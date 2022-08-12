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

package crdclient

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"

	//  import GKE cluster authentication plugin
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"

	//  import OIDC cluster authentication plugin, e.g. for Tectonic
	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
	"k8s.io/client-go/tools/cache"

	"istio.io/istio/pilot/pkg/model"
	"istio.io/istio/pkg/config"
	"istio.io/istio/pkg/config/schema/collection"
	"istio.io/istio/pkg/config/schema/gvk"
	"istio.io/pkg/log"
)

// cacheHandler abstracts the logic of an informer with a set of handlers. Handlers can be added at runtime
// and will be invoked on each informer event.
type cacheHandler struct {
	client   *Client
	informer cache.SharedIndexInformer
	schema   collection.Schema
	lister   func(namespace string) cache.GenericNamespaceLister
}

func (h *cacheHandler) onEvent(old interface{}, curr interface{}, event model.Event) error {
	currItem, ok := curr.(runtime.Object)
	if !ok {
		log.Warnf("New Object can not be converted to runtime Object %v, is type %T", curr, curr)
		return nil
	}
	currConfig := TranslateObject(currItem, h.schema.Resource().GroupVersionKind(), h.client.domainSuffix)
	if err := h.client.checkReadyForEvents(curr); err != nil {
		log.Infof("sfdclog:checkReadyForEvents the event %s with version %s %v", currConfig.Name, currConfig.ResourceVersion, err)
		return err
	}
	var oldConfig config.Config
	if old != nil {
		oldItem, ok := old.(runtime.Object)
		if !ok {
			log.Warnf("Old Object can not be converted to runtime Object %v, is type %T", old, old)
			return nil
		}
		oldConfig = TranslateObject(oldItem, h.schema.Resource().GroupVersionKind(), h.client.domainSuffix)
	}

	// TODO we may consider passing a pointer to handlers instead of the value. While spec is a pointer, the meta will be copied
	log.Infof("sfdclog:invoking %d handlers the event %s with version %s",
		len(h.client.handlers[h.schema.Resource().GroupVersionKind()]), currConfig.Name, currConfig.ResourceVersion)
	for _, f := range h.client.handlers[h.schema.Resource().GroupVersionKind()] {
		log.Infof("sfdclog:processing handler the event %s with version %s", currConfig.Name, currConfig.ResourceVersion)
		f(oldConfig, currConfig, event)
		log.Infof("sfdclog:processed handler the event %s with version %s", currConfig.Name, currConfig.ResourceVersion)
	}
	log.Infof("sfdclog:completed all handlers the event %s with version %s", currConfig.Name, currConfig.ResourceVersion)
	return nil
}

func createCacheHandler(cl *Client, schema collection.Schema, i informers.GenericInformer) *cacheHandler {
	scope.Debugf("registered CRD %v", schema.Resource().GroupVersionKind())
	h := &cacheHandler{
		client:   cl,
		schema:   schema,
		informer: i.Informer(),
	}
	h.lister = func(namespace string) cache.GenericNamespaceLister {
		if schema.Resource().IsClusterScoped() {
			return i.Lister()
		}
		return i.Lister().ByNamespace(namespace)
	}
	kind := schema.Resource().Kind()
	i.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			incrementEvent(kind, "add")
			if !cl.beginSync.Load() {
				return
			}
			cl.queue.Push(func() error {
				return h.onEvent(nil, obj, model.EventAdd)
			})
		},
		UpdateFunc: func(old, cur interface{}) {
			currItem, _ := cur.(runtime.Object)

			currConfig := TranslateObject(currItem, h.schema.Resource().GroupVersionKind(), h.client.domainSuffix)
			casamDr := false

			if currConfig.GroupVersionKind == gvk.DestinationRule && currConfig.Namespace == "core-on-sam" {
				casamDr = true
			}
			if casamDr {
				incrementEvent(kind, "update")
			}
			if !cl.beginSync.Load() {
				if casamDr {
					log.Infof("sfdclog:begin sync is not ready for the event %s with version %s", currConfig.Name, currConfig.ResourceVersion)
					incrementEvent(kind, "beginsync")
				}
				return
			}
			if casamDr {
				log.Infof("sfdclog:pushing the event to queue %s with version %s", currConfig.Name, currConfig.ResourceVersion)
			}
			cl.queue.Push(func() error {
				if casamDr {
					log.Infof("sfdclog:queue processing the event %s with version %s", currConfig.Name, currConfig.ResourceVersion)
					incrementEvent(kind, "beforeprocess")
				}
				err := h.onEvent(old, cur, model.EventUpdate)
				if casamDr {
					if err != nil {
						incrementEvent(kind, "errorevent")
					} else {
						log.Infof("sfdclog:Completed the event %s with version %s", currConfig.Name, currConfig.ResourceVersion)
						incrementEvent(kind, "completed")
					}
				}
				return err
			})
		},
		DeleteFunc: func(obj interface{}) {
			incrementEvent(kind, "delete")
			if !cl.beginSync.Load() {
				return
			}
			cl.queue.Push(func() error {
				return h.onEvent(nil, obj, model.EventDelete)
			})
		},
	})
	return h
}
