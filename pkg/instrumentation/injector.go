// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package instrumentation

import (
	"fmt"
	"os"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// Injection modes. In injector mode, the language SDKs are activated by the OpenTelemetry Injector
// (libotelinject.so, loaded via LD_PRELOAD) instead of language environment variables set here. The injector
// runs in every process of the container before the language runtime starts, sets the same variables the
// operator sets in "old" mode, and carries the ADOT start-up guard (it stops loading the SDK in a container that
// keeps failing with it).
const (
	InjectionModeOld      = "old"
	InjectionModeInjector = "injector"

	envLDPreload = "LD_PRELOAD"

	envInjectorJavaAgentPath      = "JVM_AUTO_INSTRUMENTATION_AGENT_PATH"
	envInjectorNodeJSAgentPath    = "NODEJS_AUTO_INSTRUMENTATION_AGENT_PATH"
	envInjectorPythonPathPrefix   = "PYTHON_AUTO_INSTRUMENTATION_AGENT_PATH_PREFIX"
	envInjectorDotNetPathPrefix   = "DOTNET_AUTO_INSTRUMENTATION_AGENT_PATH_PREFIX"
	injectorJavaAgentPath         = javaInstrMountPath + "/javaagent.jar"
	injectorNodeJSAgentPath       = nodejsInstrMountPath + "/adot-guard.js"
	injectorRelativeLibraryPath   = "/injector/libotelinject.so"
	injectorDefaultImageEnvPrefix = "AUTO_INSTRUMENTATION_"
)

// injectorLangs holds the languages (JAVA, NODEJS, PYTHON, DOTNET) injected through the injector. The default
// injection mode is "old": no language uses the injector unless SetInjectionMode enables it.
var injectorLangs = map[string]bool{}

var injectionModeValue = InjectionModeOld

var injectionModeLanguages = map[string]string{"java": "JAVA", "nodejs": "NODEJS", "python": "PYTHON", "dotnet": "DOTNET"}

// SetInjectionMode sets how Java, Node.js, Python and .NET are injected into Linux pods: "old" (language environment
// variables, as before), "injector" (all four through the injector), or a comma-separated list of the languages that
// use the injector (e.g. "python" or "python,nodejs"); the others keep the old injection.
func SetInjectionMode(mode string) error {
	m := strings.ToLower(strings.TrimSpace(mode))
	langs := map[string]bool{}
	switch m {
	case InjectionModeOld, "":
		m = InjectionModeOld
	case InjectionModeInjector:
		for _, l := range injectionModeLanguages {
			langs[l] = true
		}
	default:
		for _, part := range strings.Split(m, ",") {
			l, ok := injectionModeLanguages[strings.TrimSpace(part)]
			if !ok {
				return fmt.Errorf("invalid injection mode %q: want %q, %q or a comma-separated list of java, nodejs, python, dotnet", mode, InjectionModeOld, InjectionModeInjector)
			}
			langs[l] = true
		}
	}
	injectionModeValue = m
	injectorLangs = langs
	return nil
}

// GetInjectionMode returns the current injection mode as set.
func GetInjectionMode() string { return injectionModeValue }

// injectorReadyLanguages are the languages whose default instrumentation image (the one the add-on pins) carries
// the Injector. instrumentationGuard true moves only these to the injector: a language whose image lacks
// libotelinject.so would otherwise get an LD_PRELOAD that names a missing file, and the container would run without
// any SDK. Add a language here once its default image ships the Injector.
var injectorReadyLanguages = []string{"python"}

// ConfigureInjectionMode sets the injection mode from the customer switch instrumentationGuard and the internal
// --instrumentation-injection-mode flag. The internal flag is validated in every case and decides on its own when
// instrumentationGuard is false or not set (nil; default "old"). instrumentationGuard true adds the
// injectorReadyLanguages to the languages the internal flag already moves to the injector.
func ConfigureInjectionMode(instrumentationGuard *bool, internalMode string) error {
	if err := SetInjectionMode(internalMode); err != nil {
		return err
	}
	if instrumentationGuard == nil || !*instrumentationGuard || GetInjectionMode() == InjectionModeInjector {
		return nil
	}
	var langs []string
	for name, l := range injectionModeLanguages {
		if injectorLangs[l] {
			langs = append(langs, name)
		}
	}
	for _, name := range injectorReadyLanguages {
		if !injectorLangs[injectionModeLanguages[name]] {
			langs = append(langs, name)
		}
	}
	sort.Strings(langs)
	return SetInjectionMode(strings.Join(langs, ","))
}

// injectorLanguages in LD_PRELOAD preference order: the first one injected into a container provides the
// libotelinject.so that is loaded, and therefore the guard's state directory. Each language image pins its own
// Injector version, so the copies in one pod may be different versions; only the first one is loaded.
var injectorLanguages = []struct {
	lang      string // AUTO_INSTRUMENTATION_<lang> holds the operator's default image
	envName   string // set only for languages injected through the injector
	mountPath string
}{
	{"JAVA", envInjectorJavaAgentPath, javaInstrMountPath},
	{"NODEJS", envInjectorNodeJSAgentPath, nodejsInstrMountPath},
	{"PYTHON", envInjectorPythonPathPrefix, pythonInstrMountPath},
	{"DOTNET", envInjectorDotNetPathPrefix, dotnetInstrMountPath},
}

// useInjector reports whether a language is injected through the injector in this container: injector mode,
// a Linux pod, the operator's own default image for that language (only those images carry the injector; a
// customer-pinned image, e.g. an older ADOT release, keeps today's injection), and an LD_PRELOAD we can extend.
func useInjector(lang string, image string, pod corev1.Pod, container *corev1.Container) bool {
	if !injectorLangs[lang] || isWindowsPod(pod) {
		return false
	}
	def, ok := os.LookupEnv(injectorDefaultImageEnvPrefix + lang)
	if !ok || def == "" || image != def {
		return false
	}
	if idx := getIndexOfEnv(container.Env, envLDPreload); idx > -1 && container.Env[idx].ValueFrom != nil {
		return false // cannot append to a value we cannot see
	}
	return true
}

// imageCarriesInjector reports whether pods get the injector-carrying default image for a language on Linux.
func imageCarriesInjector(lang string, image string) bool {
	if !injectorLangs[lang] {
		return false
	}
	def, ok := os.LookupEnv(injectorDefaultImageEnvPrefix + lang)
	return ok && def != "" && image == def
}

// setInjectorEnv sets one injector agent-path variable (never overrides a customer value).
func setInjectorEnv(container *corev1.Container, name, value string) {
	if getIndexOfEnv(container.Env, name) == -1 {
		container.Env = append(container.Env, corev1.EnvVar{Name: name, Value: value})
	}
}

// applyInjectorPreload adds LD_PRELOAD to every container that has at least one language injected through the
// injector, pointing at the first such language's copy of libotelinject.so; an existing LD_PRELOAD is kept and
// ours is appended.
func applyInjectorPreload(pod corev1.Pod) corev1.Pod {
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		lib := ""
		for _, l := range injectorLanguages {
			if getIndexOfEnv(c.Env, l.envName) > -1 && hasVolumeMount(c, l.mountPath) {
				lib = l.mountPath + injectorRelativeLibraryPath
				break
			}
		}
		if lib == "" {
			continue
		}
		idx := getIndexOfEnv(c.Env, envLDPreload)
		switch {
		case idx == -1:
			c.Env = append(c.Env, corev1.EnvVar{Name: envLDPreload, Value: lib})
		case c.Env[idx].ValueFrom != nil:
			// not reachable: useInjector refuses such containers
		case strings.Contains(c.Env[idx].Value, lib):
			// already there (re-invocation)
		case strings.TrimSpace(c.Env[idx].Value) == "":
			c.Env[idx].Value = lib
		default:
			c.Env[idx].Value = c.Env[idx].Value + ":" + lib
		}
	}
	return pod
}

func hasVolumeMount(c *corev1.Container, path string) bool {
	for _, m := range c.VolumeMounts {
		if m.MountPath == path {
			return true
		}
	}
	return false
}
