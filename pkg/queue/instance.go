// Copyright Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package queue

import (
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"k8s.io/apimachinery/pkg/util/rand"

	"istio.io/pkg/log"
	"istio.io/pkg/monitoring"
)

// Task to be performed.
type Task func() error

type BackoffTask struct {
	task    Task
	backoff *backoff.ExponentialBackOff
}

// Instance of work tickets processed using a rate-limiting loop
type Instance interface {
	// Push a task.
	Push(task Task)
	// Run the loop until a signal on the channel
	Run(<-chan struct{})

	// Closed returns a chan that will be signaled when the Instance has stopped processing tasks.
	Closed() <-chan struct{}
}

var add = monitoring.NewSum(
	"pilot_queue_adds",
	"Total number of entries to queue.",
)

var size = monitoring.NewGauge(
	"pilot_queue_size",
	"Total number of items in queue.",
)

var done = monitoring.NewSum(
	"pilot_queue_done",
	"Total number of items processing done.",
)

var inprogress = monitoring.NewGauge(
	"pilot_queue_inprogress",
	"Total number of items in progress.",
)

func init() {
	monitoring.MustRegister(add)
	monitoring.MustRegister(size)
	monitoring.MustRegister(done)
	monitoring.MustRegister(inprogress)
}

type queueImpl struct {
	delay        time.Duration
	retryBackoff *backoff.ExponentialBackOff
	tasks        []*BackoffTask
	cond         *sync.Cond
	closing      bool
	closed       chan struct{}
	closeOnce    *sync.Once
	id           string
}

func newExponentialBackOff(eb *backoff.ExponentialBackOff) *backoff.ExponentialBackOff {
	if eb == nil {
		return nil
	}
	teb := backoff.NewExponentialBackOff()
	teb.InitialInterval = eb.InitialInterval
	teb.MaxElapsedTime = eb.MaxElapsedTime
	teb.MaxInterval = eb.MaxInterval
	teb.Multiplier = eb.Multiplier
	teb.RandomizationFactor = eb.RandomizationFactor
	return teb
}

// NewQueue instantiates a queue with a processing function
func NewQueue(errorDelay time.Duration) Instance {
	return NewQueueWithID(errorDelay, rand.String(10))
}

func NewQueueWithID(errorDelay time.Duration, name string) Instance {
	return &queueImpl{
		delay:     errorDelay,
		tasks:     make([]*BackoffTask, 0),
		closing:   false,
		closed:    make(chan struct{}),
		closeOnce: &sync.Once{},
		cond:      sync.NewCond(&sync.Mutex{}),
		id:        name,
	}
}

func NewBackOffQueue(backoff *backoff.ExponentialBackOff) Instance {
	return &queueImpl{
		retryBackoff: backoff,
		tasks:        make([]*BackoffTask, 0),
		closing:      false,
		closed:       make(chan struct{}),
		closeOnce:    &sync.Once{},
		cond:         sync.NewCond(&sync.Mutex{}),
	}
}

func (q *queueImpl) Push(item Task) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	if !q.closing {
		add.Increment()
		q.tasks = append(q.tasks, &BackoffTask{item, newExponentialBackOff(q.retryBackoff)})
		size.RecordInt(int64(len(q.tasks)))
	}
	q.cond.Signal()
}

func (q *queueImpl) pushRetryTask(item *BackoffTask) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	if !q.closing {
		q.tasks = append(q.tasks, item)
	}
	q.cond.Signal()
}

func (q *queueImpl) Closed() <-chan struct{} {
	return q.closed
}

func (q *queueImpl) Get() (*BackoffTask, bool) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	// wait for closing to be set, or a task to be pushed
	for !q.closing && len(q.tasks) == 0 {
		size.RecordInt(int64(len(q.tasks)))
		q.cond.Wait()
	}

	if len(q.tasks) == 0 {
		size.RecordInt(int64(len(q.tasks)))
		// We must be shutting down.
		return nil, true
	}
	backoffTask := q.tasks[0]
	inprogress.Increment()
	// Slicing will not free the underlying elements of the array, so explicitly clear them out here
	q.tasks[0] = nil
	q.tasks = q.tasks[1:]
	size.RecordInt(int64(len(q.tasks)))
	return backoffTask, false
}

func (q *queueImpl) processNextItem() bool {
	// Wait until there is a new item in the working queue
	backoffTask, quit := q.Get()
	if quit {
		return false
	}
	defer func() {
		inprogress.Decrement()
		done.Increment()
	}()

	// Invoke the method containing the business logic
	if err := backoffTask.task(); err != nil {
		delay := q.delay
		if q.retryBackoff != nil {
			delay = backoffTask.backoff.NextBackOff()
		}
		log.Infof("Work item handle failed (%v), retry after delay %v", err, delay)
		time.AfterFunc(delay, func() {
			q.pushRetryTask(backoffTask)
		})
	}
	return true
}

func (q *queueImpl) Run(stop <-chan struct{}) {
	log.Debugf("started queue %s", q.id)
	defer func() {
		q.closeOnce.Do(func() {
			log.Debugf("closed queue %s", q.id)
			close(q.closed)
		})
	}()
	go func() {
		<-stop
		q.cond.L.Lock()
		q.cond.Signal()
		q.closing = true
		q.cond.L.Unlock()
	}()

	for q.processNextItem() {
	}
}
