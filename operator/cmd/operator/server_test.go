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

package main

import (
	//"reflect"

	"fmt"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
)

func TestAddServerFlags(t *testing.T) {
	tests := []struct {
		desc                    string
		maxConcurrentReconciles int
	}{
		{
			desc:                    "no concurrent cmd",
			maxConcurrentReconciles: 1,
		},
		{
			desc:                    "eanble concurrent cmd",
			maxConcurrentReconciles: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			cmd := &cobra.Command{}
			args := &serverArgs{}
			args.maxConcurrentReconciles = tt.maxConcurrentReconciles
			addServerFlags(cmd, args)
			origin, _ := fmt.Printf("%+v", tt.maxConcurrentReconciles)
			actual, _ := fmt.Printf("%+v", cmd.Flag("max-concurrent-reconciles").Value)

			assert.Equal(t, origin, actual)
		})
	}
}
