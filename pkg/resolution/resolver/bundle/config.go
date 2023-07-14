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

const (
	// ConfigMapName is the bundle resolver's config map
	ConfigMapName = "bundleresolver-config"
	// ConfigServiceAccount is the configuration field name for controlling
	// the Service Account name to use for bundle requests.
	ConfigServiceAccount = "default-service-account"
	// ConfigKind is the configuration field name for controlling
	// what the layer name in the bundle image is.
	ConfigKind = "default-kind"

	// StartupPreloadPrefix is the prefix that precedes bundle images you want processed
	// on controller startup for populating the bundle cache
	StartupPreloadPrefix          = "load-on-startup-"
	StartupPreloadKindSuffix      = "-kind"
	StartupPreloadEntryNameSuffix = "-entry-name"

	// StartupPreloadServiceAccount is the name of the Service Account which has the credentials
	// needed to pull the bundle images designated for populating the bundle cache
	StartupPreloadServiceAccount = "preload-service-account"

	// StartupPreloadServiceAccountNamespace is the name of the Namespace where the preload Service Account resides.
	StartupPreloadServiceAccountNamespace = "preload-namespace"

	// StartupCacheLoadOnRequest is the key for a boolean which tells the cache system to load entries to the cache
	// after an initial pull of the image; this is an alternative to having to specify every potential bundle in the
	// config map for loading at start up.
	StartupCacheLoadOnRequest = "load-on-first-request"
)
