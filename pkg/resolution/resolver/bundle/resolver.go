/*
 Copyright 2022 The Tekton Authors

 Licensed under the Apache License, Version 2.0 (the "License");
 you may not use this file except in compliance with the License.
 You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

 Unless required by applicable law or agreed to in writing, software
 distributed under the License is distributed on an "AS IS" BASIS,
 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 See the License for the specific language governing permissions and
 limitations under the License.
*/

package bundle

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/authn/k8schain"
	resolverconfig "github.com/tektoncd/pipeline/pkg/apis/config/resolver"
	pipelinev1beta1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"github.com/tektoncd/pipeline/pkg/resolution/common"
	"github.com/tektoncd/pipeline/pkg/resolution/resolver/framework"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"knative.dev/pkg/client/injection/kube/client"
	"knative.dev/pkg/configmap"
	"knative.dev/pkg/configmap/informer"
	"knative.dev/pkg/logging"
)

const (
	disabledError = "cannot handle resolution request, enable-bundles-resolver feature flag not true"

	// LabelValueBundleResolverType is the value to use for the
	// resolution.tekton.dev/type label on resource requests
	LabelValueBundleResolverType string = "bundles"

	// TODO(sbwsg): This should be exposed as a configurable option for
	// admins (e.g. via ConfigMap)
	timeoutDuration = time.Minute

	// BundleResolverName is the name that the bundle resolver should be associated with.
	BundleResolverName = "bundleresolver"
)

// Resolver implements a framework.Resolver that can fetch files from OCI bundles.
type Resolver struct {
	kubeClientSet kubernetes.Interface
}

// Initialize sets up any dependencies needed by the Resolver. None atm.
func (r *Resolver) Initialize(ctx context.Context) error {
	r.kubeClientSet = client.Get(ctx)
	return nil
}

// GetName returns a string name to refer to this Resolver by.
func (r *Resolver) GetName(context.Context) string {
	return BundleResolverName
}

// GetConfigName returns the name of the bundle resolver's configmap.
func (r *Resolver) GetConfigName(context.Context) string {
	return ConfigMapName
}

// StartupInitialization allows for a resolver to perform operations from the configuration on startup
func (r *Resolver) StartupInitialization(ctx context.Context, cmw configmap.Watcher) error {
	if informerWatcher, ok := cmw.(*informer.InformedWatcher); ok {
		informerWatcher.Watch(ConfigMapName, func(configMap *corev1.ConfigMap) {
			ctx = framework.InjectResolverConfigToContext(ctx, configMap.Data)
			r.populateCache(ctx)
		})
	}
	return r.populateCache(ctx)
}

func (r *Resolver) populateCache(ctx context.Context) error {
	imgCache.Purge()
	config := framework.GetResolverConfigFromContext(ctx)
	var e error
	sa, _ := config[StartupPreloadServiceAccount]
	ns, _ := config[StartupPreloadServiceAccountNamespace]
	for key, image := range config {
		if strings.HasPrefix(key, StartupPreloadPrefix) {
			if len(image) == 0 {
				continue
			}
			kind, kok := config[key+StartupPreloadKindSuffix]
			if !kok {
				continue
			}
			entryName, eok := config[key+StartupPreloadEntryNameSuffix]
			if !eok {
				continue
			}
			kc, _ := k8schain.New(ctx, r.kubeClientSet, k8schain.Options{
				Namespace:          ns,
				ServiceAccountName: sa,
			})
			opts := RequestOptions{
				Bundle:    image,
				Kind:      kind,
				EntryName: entryName,
			}
			err := getEntry(ctx, kc, opts)
			if err != nil {
				e = err
				logging.FromContext(ctx).Infof("cache preload for %s got error: %s", image, err.Error())
			}
		}
	}
	return e
}

func getEntry(ctx context.Context, kc authn.Keychain, opts RequestOptions) error {
	c, cancelFunc := context.WithTimeout(ctx, timeoutDuration)
	defer cancelFunc()
	_, err := GetEntry(c, kc, opts)
	return err
}

// GetSelector returns a map of labels to match requests to this Resolver.
func (r *Resolver) GetSelector(context.Context) map[string]string {
	return map[string]string{
		common.LabelKeyResolverType: LabelValueBundleResolverType,
	}
}

// ValidateParams ensures parameters from a request are as expected.
func (r *Resolver) ValidateParams(ctx context.Context, params []pipelinev1beta1.Param) error {
	if r.isDisabled(ctx) {
		return errors.New(disabledError)
	}
	if _, err := OptionsFromParams(ctx, params); err != nil {
		return err
	}
	return nil
}

// Resolve uses the given params to resolve the requested file or resource.
func (r *Resolver) Resolve(ctx context.Context, params []pipelinev1beta1.Param) (framework.ResolvedResource, error) {
	if r.isDisabled(ctx) {
		return nil, errors.New(disabledError)
	}
	opts, err := OptionsFromParams(ctx, params)
	if err != nil {
		return nil, err
	}
	namespace := common.RequestNamespace(ctx)
	// down in the knative dependencies, a non-caching client direct hit to the api server i.e. no caching client; so, we do thecache lookup
	// here vs. down in GetEntry
	key := ""
	key, err = getImageCacheKey(opts.Bundle)
	if err != nil {
		return nil, err
	}

	if rr, ok := imgCache.Get(key); ok {
		logging.FromContext(ctx).Infof("Bundle.Resolve found cache entry for: %s", key)
		return rr.(*ResolvedResource), nil
	} else {
		logging.FromContext(ctx).Infof("Bundle.Resolve no cache entry for: %s", key)
	}

	kc, _ := k8schain.New(ctx, r.kubeClientSet, k8schain.Options{
		Namespace:          namespace,
		ServiceAccountName: opts.ServiceAccount,
	})
	ctx, cancelFn := context.WithTimeout(ctx, timeoutDuration)
	defer cancelFn()
	return GetEntry(ctx, kc, opts)
}

func (r *Resolver) isDisabled(ctx context.Context) bool {
	cfg := resolverconfig.FromContextOrDefaults(ctx)
	return !cfg.FeatureFlags.EnableBundleResolver
}
