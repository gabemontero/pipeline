/*
Copyright 2019 The Tekton Authors

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

package main

import (
	"context"
	"flag"
	//"fmt"
	"log"
	"net/http"
	"os"
	//"reflect"
	//"sync"
	//"time"

	"github.com/tektoncd/pipeline/pkg/apis/pipeline"
	//"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	//"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1alpha1"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	"github.com/tektoncd/pipeline/pkg/reconciler/customrun"
	"github.com/tektoncd/pipeline/pkg/reconciler/pipelinerun"
	"github.com/tektoncd/pipeline/pkg/reconciler/resolutionrequest"
	"github.com/tektoncd/pipeline/pkg/reconciler/run"
	"github.com/tektoncd/pipeline/pkg/reconciler/taskrun"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/clock"
	filteredinformerfactory "knative.dev/pkg/client/injection/kube/informers/factory/filtered"
	"knative.dev/pkg/controller"
	"knative.dev/pkg/injection"
	"knative.dev/pkg/injection/sharedmain"
	"knative.dev/pkg/signals"

	apispipelinev1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	tektonversionedclientset "github.com/tektoncd/pipeline/pkg/client/clientset/versioned"
	//extverinformerfactory "github.com/tektoncd/pipeline/pkg/client/informers/externalversions"
	//internalinterfaces "github.com/tektoncd/pipeline/pkg/client/informers/externalversions/internalinterfaces"
	//extverinformerpipeline "github.com/tektoncd/pipeline/pkg/client/informers/externalversions/pipeline"
	tektonclientinjection "github.com/tektoncd/pipeline/pkg/client/injection/client"
	//informerfactoryinjection "github.com/tektoncd/pipeline/pkg/client/injection/informers/factory"
	pipelineruninfomerv1injection "github.com/tektoncd/pipeline/pkg/client/injection/informers/pipeline/v1/pipelinerun"
	pipelinev1 "github.com/tektoncd/pipeline/pkg/client/listers/pipeline/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	//"k8s.io/apimachinery/pkg/runtime/schema"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	kcptripper "github.com/kcp-dev/apimachinery/pkg/client"
	"k8s.io/klog/v2"
	kcpsharedinformer "github.com/kcp-dev/apimachinery/third_party/informers"
)

const (
	// ControllerLogKey is the name of the logger for the controller cmd
	ControllerLogKey = "tekton-pipelines-controller"
)

func main() {
	flag.IntVar(&controller.DefaultThreadsPerController, "threads-per-controller", controller.DefaultThreadsPerController, "Threads (goroutines) to create per controller")
	namespace := flag.String("namespace", corev1.NamespaceAll, "Namespace to restrict informer to. Optional, defaults to all namespaces.")
	disableHighAvailability := flag.Bool("disable-ha", false, "Whether to disable high-availability functionality for this component.  This flag will be deprecated "+
		"and removed when we have promoted this feature to stable, so do not pass it without filing an "+
		"issue upstream!")

	opts := &pipeline.Options{}
	flag.StringVar(&opts.Images.EntrypointImage, "entrypoint-image", "", "The container image containing our entrypoint binary.")
	flag.StringVar(&opts.Images.NopImage, "nop-image", "", "The container image used to stop sidecars")
	flag.StringVar(&opts.Images.GitImage, "git-image", "", "The container image containing our Git binary.")
	flag.StringVar(&opts.Images.KubeconfigWriterImage, "kubeconfig-writer-image", "", "The container image containing our kubeconfig writer binary.")
	flag.StringVar(&opts.Images.ShellImage, "shell-image", "", "The container image containing a shell")
	flag.StringVar(&opts.Images.ShellImageWin, "shell-image-win", "", "The container image containing a windows shell")
	flag.StringVar(&opts.Images.GsutilImage, "gsutil-image", "", "The container image containing gsutil")
	flag.StringVar(&opts.Images.PRImage, "pr-image", "", "The container image containing our PR binary.")
	flag.StringVar(&opts.Images.ImageDigestExporterImage, "imagedigest-exporter-image", "", "The container image containing our image digest exporter binary.")
	flag.StringVar(&opts.Images.WorkingDirInitImage, "workingdirinit-image", "", "The container image containing our working dir init binary.")

	// This parses flags.
	cfg := injection.ParseAndGetRESTConfigOrDie()

	if err := opts.Images.Validate(); err != nil {
		log.Fatal(err)
	}
	if cfg.QPS == 0 {
		cfg.QPS = 2 * rest.DefaultQPS
	}
	if cfg.Burst == 0 {
		cfg.Burst = rest.DefaultBurst
	}
	// FIXME(vdemeester): this is here to not break current behavior
	// multiply by 2, no of controllers being created
	cfg.QPS = 2 * cfg.QPS
	cfg.Burst = 2 * cfg.Burst

	ctx := injection.WithNamespaceScope(signals.NewContext(), *namespace)
	if *disableHighAvailability {
		ctx = sharedmain.WithHADisabled(ctx)
	}

	// sets up liveness and readiness probes.
	mux := http.NewServeMux()

	mux.HandleFunc("/", handler)
	mux.HandleFunc("/health", handler)
	mux.HandleFunc("/readiness", handler)

	port := os.Getenv("PROBES_PORT")
	if port == "" {
		port = "8080"
	}

	go func() {
		// start the web server on port and accept requests
		log.Printf("Readiness and health check server listening on port %s", port)
		log.Fatal(http.ListenAndServe(":"+port, mux))
	}()

	ctx = filteredinformerfactory.WithSelectors(ctx, v1beta1.ManagedByLabelKey)
	trctrl := taskrun.NewController(opts, clock.RealClock{})
	prctrl := pipelinerun.NewController(opts, clock.RealClock{})
	rrctrl := resolutionrequest.NewController(clock.RealClock{})
	crctrl := customrun.NewController()
	rctrl := run.NewController()

	//let's insert our new injections before starting the controllers
	httpclient, err := ClusterAwareHTTPClient(cfg)
	if err != nil {

	}
	allversionclientset, err2 := tektonversionedclientset.NewForConfigAndClient(cfg, httpclient)
	klog.Infof("GGM new client %#v", allversionclientset)
	if err2 != nil {

	}
	// start registering injections using allversionclientset as the base.
	cf := func(ctx context.Context, config *rest.Config) context.Context {
		return context.WithValue(ctx, tektonclientinjection.Key{}, allversionclientset)
	}
	klog.Infof("GGM main client register context func %#v", cf)
	injection.Default.RegisterClient(cf)

	informerInjector := func(ctx context.Context) (context.Context, controller.Informer) {
		inf := &prwrapper{client: allversionclientset, resourceVersion: injection.GetResourceVersion(ctx)}
		klog.Infof("GGM new override informer injection client")
		return context.WithValue(ctx, pipelineruninfomerv1injection.Key{}, inf), inf.Informer()
	}
	klog.Infof("GGM main informer register context func %#v", informerInjector)
	injection.Default.RegisterInformer(informerInjector)
	//informerFactoryInjector := func(ctx context.Context) context.Context {
	//	opts := make([]SharedInformerOption, 0, 1)
	//	//TODO if want to limit scope to ns, then add that code
	//	return context.WithValue(ctx, informerfactoryinjection.Key{}, NewSharedInformerFactoryWithOptions(allversionclientset, controller.GetResyncPeriod(ctx), opts...))
	//}
	//klog.Infof("GGM main informer factory registry context func %#v", informerFactoryInjector)
	//injection.Default.RegisterInformerFactory(informerFactoryInjector)

	//now, when these controllers call ...Get(ctx) to get clients they get our overridden client
	sharedmain.MainWithConfig(ctx, ControllerLogKey, cfg,
		trctrl,
		prctrl,
		rctrl,
		rrctrl,
		crctrl,
	)
}

func handler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// kcp / knative override prototype (long term will live somewhere else

// ClusterAwareHTTPClient returns an http.Client with a cluster aware round tripper.
func ClusterAwareHTTPClient(config *rest.Config) (*http.Client, error) {
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, err
	}

	httpClient.Transport = kcptripper.NewClusterRoundTripper(httpClient.Transport)
	return httpClient, nil
}

type prwrapper struct {
	client tektonversionedclientset.Interface

	namespace string

	resourceVersion string
}

func (w *prwrapper) Informer() cache.SharedIndexInformer {
	//FYI - the reset of the controllers are still doing v1beta1, but I picked v1 to help distinguish.
	klog.Infof("GGM prwrapper Informer returning kcp NewSharedIndexInformer with version specific listerwatcher")
	listerWatcher := &cache.ListWatch{
		ListFunc:        func(opts metav1.ListOptions) (kruntime.Object, error) {
			klog.Infof("GGMGGM informer calling our lister")
			//TODO if want mimic namespace scoping cfg option noted above
			return w.client.TektonV1().PipelineRuns(metav1.NamespaceAll).List(context.Background(), opts)
		},
		WatchFunc:       func(opts metav1.ListOptions) (watch.Interface, error) {
			klog.Infof("GGMGGM informer calling our watcher")
			//TODO see above
			return w.client.TektonV1().PipelineRuns(metav1.NamespaceAll).Watch(context.Background(), opts)
		},
	}
	return kcpsharedinformer.NewSharedIndexInformer(listerWatcher, &apispipelinev1.PipelineRun{}, 0, nil)
}

func (w *prwrapper) Lister() pipelinev1.PipelineRunLister {
	return w
}

func (w *prwrapper) PipelineRuns(namespace string) pipelinev1.PipelineRunNamespaceLister {
	return &prwrapper{client: w.client, namespace: namespace, resourceVersion: w.resourceVersion}
}

// SetResourceVersion allows consumers to adjust the minimum resourceVersion
// used by the underlying client.  It is not accessible via the standard
// lister interface, but can be accessed through a user-defined interface and
// an implementation check e.g. rvs, ok := foo.(ResourceVersionSetter)
func (w *prwrapper) SetResourceVersion(resourceVersion string) {
	w.resourceVersion = resourceVersion
}

func (w *prwrapper) List(selector labels.Selector) (ret []*apispipelinev1.PipelineRun, err error) {
	lo, err := w.client.TektonV1().PipelineRuns(w.namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector:   selector.String(),
		ResourceVersion: w.resourceVersion,
	})
	if err != nil {
		return nil, err
	}
	for idx := range lo.Items {
		ret = append(ret, &lo.Items[idx])
	}
	return ret, nil
}

func (w *prwrapper) Get(name string) (*apispipelinev1.PipelineRun, error) {
	return w.client.TektonV1().PipelineRuns(w.namespace).Get(context.TODO(), name, metav1.GetOptions{
		ResourceVersion: w.resourceVersion,
	})
}

//type sharedInformerFactory struct {
//	client           tektonversionedclientset.Interface
//	namespace        string
//	tweakListOptions internalinterfaces.TweakListOptionsFunc
//	lock             sync.Mutex
//	defaultResync    time.Duration
//	customResync     map[reflect.Type]time.Duration
//
//	informers map[reflect.Type]cache.SharedIndexInformer
//	// startedInformers is used for tracking which informers have been started.
//	// This allows Start() to be called multiple times safely.
//	startedInformers map[reflect.Type]bool
//}
//
//// Start initializes all requested informers.
//func (f *sharedInformerFactory) Start(stopCh <-chan struct{}) {
//	f.lock.Lock()
//	defer f.lock.Unlock()
//
//	for informerType, informer := range f.informers {
//		if !f.startedInformers[informerType] {
//			go informer.Run(stopCh)
//			f.startedInformers[informerType] = true
//		}
//	}
//}
//
//// WaitForCacheSync waits for all started informers' cache were synced.
//func (f *sharedInformerFactory) WaitForCacheSync(stopCh <-chan struct{}) map[reflect.Type]bool {
//	informers := func() map[reflect.Type]cache.SharedIndexInformer {
//		f.lock.Lock()
//		defer f.lock.Unlock()
//
//		informers := map[reflect.Type]cache.SharedIndexInformer{}
//		for informerType, informer := range f.informers {
//			if f.startedInformers[informerType] {
//				informers[informerType] = informer
//			}
//		}
//		return informers
//	}()
//
//	res := map[reflect.Type]bool{}
//	for informType, informer := range informers {
//		res[informType] = cache.WaitForCacheSync(stopCh, informer.HasSynced)
//	}
//	return res
//}
//
//// InternalInformerFor returns the SharedIndexInformer for obj using an internal
//// client.
//func (f *sharedInformerFactory) InformerFor(obj kruntime.Object, newFunc internalinterfaces.NewInformerFunc) cache.SharedIndexInformer {
//	f.lock.Lock()
//	defer f.lock.Unlock()
//
//	informerType := reflect.TypeOf(obj)
//	informer, exists := f.informers[informerType]
//	if exists {
//		return informer
//	}
//
//	resyncPeriod, exists := f.customResync[informerType]
//	if !exists {
//		resyncPeriod = f.defaultResync
//	}
//
//	informer = newFunc(f.client, resyncPeriod)
//	f.informers[informerType] = informer
//
//	return informer
//}
//
//func (f *sharedInformerFactory) Tekton() extverinformerpipeline.Interface {
//	return extverinformerpipeline.New(f, f.namespace, f.tweakListOptions)
//}
//
//// ForResource gives generic access to a shared informer of the matching type
//// TODO extend this to unknown resources with a client pool
//func (f *sharedInformerFactory) ForResource(resource schema.GroupVersionResource) (extverinformerfactory.GenericInformer, error) {
//	switch resource {
//	// Group=tekton.dev, Version=v1
//	case v1.SchemeGroupVersion.WithResource("pipelines"):
//		return &genericInformer{resource: resource.GroupResource(), informer: f.Tekton().V1().Pipelines().Informer()}, nil
//	case v1.SchemeGroupVersion.WithResource("pipelineruns"):
//		return &genericInformer{resource: resource.GroupResource(), informer: f.Tekton().V1().PipelineRuns().Informer()}, nil
//	case v1.SchemeGroupVersion.WithResource("tasks"):
//		return &genericInformer{resource: resource.GroupResource(), informer: f.Tekton().V1().Tasks().Informer()}, nil
//	case v1.SchemeGroupVersion.WithResource("taskruns"):
//		return &genericInformer{resource: resource.GroupResource(), informer: f.Tekton().V1().TaskRuns().Informer()}, nil
//
//		// Group=tekton.dev, Version=v1alpha1
//	case v1alpha1.SchemeGroupVersion.WithResource("runs"):
//		return &genericInformer{resource: resource.GroupResource(), informer: f.Tekton().V1alpha1().Runs().Informer()}, nil
//
//		// Group=tekton.dev, Version=v1beta1
//	case v1beta1.SchemeGroupVersion.WithResource("clustertasks"):
//		return &genericInformer{resource: resource.GroupResource(), informer: f.Tekton().V1beta1().ClusterTasks().Informer()}, nil
//	case v1beta1.SchemeGroupVersion.WithResource("customruns"):
//		return &genericInformer{resource: resource.GroupResource(), informer: f.Tekton().V1beta1().CustomRuns().Informer()}, nil
//	case v1beta1.SchemeGroupVersion.WithResource("pipelines"):
//		return &genericInformer{resource: resource.GroupResource(), informer: f.Tekton().V1beta1().Pipelines().Informer()}, nil
//	case v1beta1.SchemeGroupVersion.WithResource("pipelineruns"):
//		return &genericInformer{resource: resource.GroupResource(), informer: f.Tekton().V1beta1().PipelineRuns().Informer()}, nil
//	case v1beta1.SchemeGroupVersion.WithResource("tasks"):
//		return &genericInformer{resource: resource.GroupResource(), informer: f.Tekton().V1beta1().Tasks().Informer()}, nil
//	case v1beta1.SchemeGroupVersion.WithResource("taskruns"):
//		return &genericInformer{resource: resource.GroupResource(), informer: f.Tekton().V1beta1().TaskRuns().Informer()}, nil
//
//	}
//
//	return nil, fmt.Errorf("no informer found for %v", resource)
//}
//
//type genericInformer struct {
//	informer cache.SharedIndexInformer
//	resource schema.GroupResource
//}
//
//// Informer returns the SharedIndexInformer.
//func (f *genericInformer) Informer() cache.SharedIndexInformer {
//	return f.informer
//}
//
//// Lister returns the GenericLister.
//func (f *genericInformer) Lister() cache.GenericLister {
//	return cache.NewGenericLister(f.Informer().GetIndexer(), f.resource)
//}
//
//type SharedInformerOption func(*sharedInformerFactory) *sharedInformerFactory
//
//// NewSharedInformerFactoryWithOptions constructs a new instance of a SharedInformerFactory with additional options.
//func NewSharedInformerFactoryWithOptions(client tektonversionedclientset.Interface, defaultResync time.Duration, options ...SharedInformerOption) extverinformerfactory.SharedInformerFactory {
//	factory := &sharedInformerFactory{
//		client:           client,
//		namespace:        metav1.NamespaceAll,
//		defaultResync:    defaultResync,
//		informers:        make(map[reflect.Type]cache.SharedIndexInformer),
//		startedInformers: make(map[reflect.Type]bool),
//		customResync:     make(map[reflect.Type]time.Duration),
//	}
//
//	// Apply all options
//	for _, opt := range options {
//		factory = opt(factory)
//	}
//
//	return factory
//}
