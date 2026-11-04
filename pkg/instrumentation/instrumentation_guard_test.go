// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package instrumentation

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	"github.com/aws/amazon-cloudwatch-agent-operator/apis/v1alpha1"
)

func boolPtr(b bool) *bool { return &b }

// configureGuard is setInjectorDefaults for the customer switch: default images set, mode from
// instrumentationGuard and the internal flag, restored after the test.
func configureGuard(t *testing.T, guard *bool, internal string) {
	t.Helper()
	setInjectorDefaults(t, InjectionModeOld)
	require.NoError(t, ConfigureInjectionMode(guard, internal))
}

func TestConfigureInjectionModePrecedence(t *testing.T) {
	prev := GetInjectionMode()
	t.Cleanup(func() { _ = SetInjectionMode(prev) })
	all := map[string]bool{"JAVA": true, "NODEJS": true, "PYTHON": true, "DOTNET": true}
	tests := []struct {
		name      string
		guard     *bool
		internal  string
		wantMode  string
		wantLangs map[string]bool
	}{
		{"unset, internal default", nil, "", InjectionModeOld, map[string]bool{}},
		{"unset, internal old", nil, InjectionModeOld, InjectionModeOld, map[string]bool{}},
		{"false, internal old", boolPtr(false), InjectionModeOld, InjectionModeOld, map[string]bool{}},
		{"true, internal old", boolPtr(true), InjectionModeOld, InjectionModeInjector, all},
		{"true, internal default", boolPtr(true), "", InjectionModeInjector, all},
		{"unset, internal injector", nil, InjectionModeInjector, InjectionModeInjector, all},
		{"false, internal injector", boolPtr(false), InjectionModeInjector, InjectionModeInjector, all},
		{"unset, internal python", nil, "python", "python", map[string]bool{"PYTHON": true}},
		{"false, internal python", boolPtr(false), "python", "python", map[string]bool{"PYTHON": true}},
		{"true, internal python: all four", boolPtr(true), "python", InjectionModeInjector, all},
		{"true, internal injector", boolPtr(true), InjectionModeInjector, InjectionModeInjector, all},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, ConfigureInjectionMode(tt.guard, tt.internal))
			assert.Equal(t, tt.wantMode, GetInjectionMode())
			assert.Equal(t, tt.wantLangs, injectorLangs)
		})
	}
	// an invalid internal flag is refused whatever instrumentationGuard says
	for _, g := range []*bool{nil, boolPtr(false), boolPtr(true)} {
		assert.Error(t, ConfigureInjectionMode(g, "ruby"))
	}
}

func TestInstrumentationGuardTrueAllFourLanguagesThroughInjector(t *testing.T) {
	configureGuard(t, boolPtr(true), InjectionModeOld)
	p := injectAll(t, basePod(), 0, defJava, defNode, defPython, defDotNet)
	e := envOf(p.Spec.Containers[0])
	assert.Equal(t, "/otel-auto-instrumentation-java/injector/libotelinject.so", e["LD_PRELOAD"])
	for _, k := range []string{envInjectorJavaAgentPath, envInjectorNodeJSAgentPath, envInjectorPythonPathPrefix, envInjectorDotNetPathPrefix} {
		assert.Contains(t, e, k)
	}
	for _, k := range []string{"JAVA_TOOL_OPTIONS", "NODE_OPTIONS", "PYTHONPATH", "CORECLR_PROFILER_PATH"} {
		assert.NotContains(t, e, k)
	}
}

func TestInstrumentationGuardUnsetOrFalseIsOldInjection(t *testing.T) {
	setInjectorDefaults(t, InjectionModeOld)
	want := injectAll(t, basePod(), 0, defJava, defNode, defPython, defDotNet)
	for _, g := range []*bool{nil, boolPtr(false)} {
		require.NoError(t, ConfigureInjectionMode(g, InjectionModeOld))
		got := injectAll(t, basePod(), 0, defJava, defNode, defPython, defDotNet)
		assert.Equal(t, want, got)
		assert.NotContains(t, envOf(got.Spec.Containers[0]), "LD_PRELOAD")
	}
}

func TestInstrumentationGuardTrueKeepsOldInjectionForExceptions(t *testing.T) {
	// what injector mode leaves on today's injection stays there with instrumentationGuard true
	old := func(build func() corev1.Pod, java, node, python, dotnet string) (corev1.Pod, corev1.Pod) {
		require.NoError(t, ConfigureInjectionMode(nil, InjectionModeOld))
		want := injectAll(t, build(), 0, java, node, python, dotnet)
		require.NoError(t, ConfigureInjectionMode(boolPtr(true), InjectionModeOld))
		got := injectAll(t, build(), 0, java, node, python, dotnet)
		return want, got
	}
	setInjectorDefaults(t, InjectionModeOld)

	t.Run("Windows pod", func(t *testing.T) {
		want, got := old(func() corev1.Pod {
			p := basePod()
			p.Spec.NodeSelector = map[string]string{"kubernetes.io/os": "windows"}
			return p
		}, defJava, "", "", defDotNet)
		assert.Equal(t, want, got)
		assert.NotContains(t, envOf(got.Spec.Containers[0]), "LD_PRELOAD")
	})
	t.Run("custom SDK images", func(t *testing.T) {
		want, got := old(func() corev1.Pod { return basePod() }, "custom/java:1", "custom/node:1", "custom/python:1", "custom/dotnet:1")
		assert.Equal(t, want, got)
		assert.NotContains(t, envOf(got.Spec.Containers[0]), "LD_PRELOAD")
	})
	t.Run("LD_PRELOAD from valueFrom", func(t *testing.T) {
		want, got := old(func() corev1.Pod {
			return basePod(corev1.Container{Name: "app", Env: []corev1.EnvVar{{Name: "LD_PRELOAD", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "k"}}}}})
		}, "", "", defPython, "")
		assert.Equal(t, want, got)
		assert.Contains(t, envOf(got.Spec.Containers[0]), "PYTHONPATH")
	})
}

// TestInstrumentationGuardThroughPodMutator runs the webhook's pod mutator (default instrumentation, no
// Instrumentation resource) for pods annotated by auto-monitor (inject-<lang> plus auto-annotate-<lang>) and by the
// customer (inject-<lang> only): instrumentationGuard applies to both the same way.
func TestInstrumentationGuardThroughPodMutator(t *testing.T) {
	mutator := NewMutator(logr.Discard(), k8sClient, record.NewFakeRecorder(100))
	langs := []struct {
		typ     Type
		oldEnv  string
		injEnv  string
		library string
	}{
		{TypeJava, "JAVA_TOOL_OPTIONS", envInjectorJavaAgentPath, javaInstrMountPath + injectorRelativeLibraryPath},
		{TypeNodeJS, "NODE_OPTIONS", envInjectorNodeJSAgentPath, nodejsInstrMountPath + injectorRelativeLibraryPath},
		{TypePython, "PYTHONPATH", envInjectorPythonPathPrefix, pythonInstrMountPath + injectorRelativeLibraryPath},
		{TypeDotNet, "CORECLR_PROFILER_PATH", envInjectorDotNetPathPrefix, dotnetInstrMountPath + injectorRelativeLibraryPath},
	}
	sources := map[string]func(Type) map[string]string{
		"auto-monitored": func(l Type) map[string]string {
			return map[string]string{InjectAnnotationKey(l): "true", "cloudwatch.aws.amazon.com/auto-annotate-" + string(l): "true"}
		},
		"customer-annotated": func(l Type) map[string]string {
			return map[string]string{InjectAnnotationKey(l): "true"}
		},
	}
	guards := []struct {
		name     string
		guard    *bool
		internal string
		injector bool
	}{
		{"unset", nil, InjectionModeOld, false},
		{"false", boolPtr(false), InjectionModeOld, false},
		{"true", boolPtr(true), InjectionModeOld, true},
		{"false with internal injector", boolPtr(false), InjectionModeInjector, true},
	}
	n := 0
	for _, g := range guards {
		for src, annotations := range sources {
			for _, l := range langs {
				t.Run(fmt.Sprintf("%s/%s/%s", g.name, src, l.typ), func(t *testing.T) {
					overrideFeatureFlags(t)
					configureGuard(t, g.guard, g.internal)
					n++
					ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("guard-%d", n)}}
					require.NoError(t, k8sClient.Create(context.Background(), &ns))
					defer func() { _ = k8sClient.Delete(context.Background(), &ns) }()
					pod := corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns.Name, Annotations: annotations(l.typ)},
						Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
					}
					got, err := mutator.Mutate(context.Background(), ns, pod)
					require.NoError(t, err)
					e := envOf(got.Spec.Containers[0])
					if g.injector {
						assert.Equal(t, l.library, e["LD_PRELOAD"])
						assert.Contains(t, e, l.injEnv)
						assert.NotContains(t, e, l.oldEnv)
					} else {
						assert.NotContains(t, e, "LD_PRELOAD")
						assert.NotContains(t, e, l.injEnv)
						assert.Contains(t, e, l.oldEnv)
					}
				})
			}
		}
	}
}

// TestInstrumentationGuardCustomImageThroughPodMutator: an Instrumentation resource pinning a non-default image keeps
// today's injection with instrumentationGuard true.
func TestInstrumentationGuardCustomImageThroughPodMutator(t *testing.T) {
	mutator := NewMutator(logr.Discard(), k8sClient, record.NewFakeRecorder(100))
	overrideFeatureFlags(t)
	configureGuard(t, boolPtr(true), InjectionModeOld)
	ns := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "guard-custom-image"}}
	require.NoError(t, k8sClient.Create(context.Background(), &ns))
	defer func() { _ = k8sClient.Delete(context.Background(), &ns) }()
	inst := v1alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{Name: "custom", Namespace: ns.Name},
		Spec:       v1alpha1.InstrumentationSpec{Python: v1alpha1.Python{Image: "custom/python:1"}},
	}
	require.NoError(t, k8sClient.Create(context.Background(), &inst))
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns.Name, Annotations: map[string]string{annotationInjectPython: "true"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	got, err := mutator.Mutate(context.Background(), ns, pod)
	require.NoError(t, err)
	e := envOf(got.Spec.Containers[0])
	assert.Contains(t, e, "PYTHONPATH")
	assert.NotContains(t, e, "LD_PRELOAD")
	assert.NotContains(t, e, envInjectorPythonPathPrefix)
}
