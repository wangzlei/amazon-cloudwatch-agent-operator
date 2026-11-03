// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package instrumentation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	"github.com/aws/amazon-cloudwatch-agent-operator/apis/v1alpha1"
)

const (
	defJava   = "adot/java:inj"
	defNode   = "adot/node:inj"
	defPython = "adot/python:inj"
	defDotNet = "adot/dotnet:inj"
)

func setInjectorDefaults(t *testing.T, mode string) {
	t.Helper()
	t.Setenv("AUTO_INSTRUMENTATION_JAVA", defJava)
	t.Setenv("AUTO_INSTRUMENTATION_NODEJS", defNode)
	t.Setenv("AUTO_INSTRUMENTATION_PYTHON", defPython)
	t.Setenv("AUTO_INSTRUMENTATION_DOTNET", defDotNet)
	prev := GetInjectionMode()
	require.NoError(t, SetInjectionMode(mode))
	t.Cleanup(func() { _ = SetInjectionMode(prev) })
}

func envOf(c corev1.Container) map[string]string {
	m := map[string]string{}
	for _, e := range c.Env {
		m[e.Name] = e.Value
	}
	return m
}

func basePod(containers ...corev1.Container) corev1.Pod {
	if len(containers) == 0 {
		containers = []corev1.Container{{Name: "app"}}
	}
	return corev1.Pod{Spec: corev1.PodSpec{Containers: containers}}
}

func injectAll(t *testing.T, pod corev1.Pod, index int, java, node, python, dotnet string) corev1.Pod {
	t.Helper()
	var err error
	if java != "" {
		pod, err = injectJavaagent(v1alpha1.Java{Image: java}, pod, index, nil)
		require.NoError(t, err)
	}
	if node != "" {
		pod, err = injectNodeJSSDK(v1alpha1.NodeJS{Image: node}, pod, index, nil)
		require.NoError(t, err)
	}
	if python != "" {
		pod, err = injectPythonSDK(v1alpha1.Python{Image: python}, pod, index, nil)
		require.NoError(t, err)
	}
	if dotnet != "" {
		pod, err = injectDotNetSDK(v1alpha1.DotNet{Image: dotnet}, pod, index, "", nil)
		require.NoError(t, err)
	}
	return applyInjectorPreload(pod)
}

func TestDefaultInjectionModeIsOld(t *testing.T) {
	// The feature ships switched off: until the operator is started with another mode, no language uses the
	// injector.
	assert.Equal(t, InjectionModeOld, injectionModeValue)
	assert.Empty(t, injectorLangs)
	prev := GetInjectionMode()
	defer func() { _ = SetInjectionMode(prev) }()
	require.NoError(t, SetInjectionMode(""))
	assert.Equal(t, InjectionModeOld, GetInjectionMode())
	assert.Empty(t, injectorLangs)
}

func TestSetInjectionMode(t *testing.T) {
	prev := GetInjectionMode()
	defer func() { _ = SetInjectionMode(prev) }()
	assert.NoError(t, SetInjectionMode("old"))
	assert.NoError(t, SetInjectionMode("injector"))
	assert.Error(t, SetInjectionMode("other"))
	assert.Equal(t, InjectionModeInjector, GetInjectionMode())
}

func TestInjectorModeSingleLanguages(t *testing.T) {
	setInjectorDefaults(t, InjectionModeInjector)

	py := injectAll(t, basePod(), 0, "", "", defPython, "")
	e := envOf(py.Spec.Containers[0])
	assert.NotContains(t, e, "PYTHONPATH")
	assert.Equal(t, "/otel-auto-instrumentation-python", e[envInjectorPythonPathPrefix])
	assert.Equal(t, "/otel-auto-instrumentation-python/injector/libotelinject.so", e["LD_PRELOAD"])
	// unchanged config env
	assert.Equal(t, "otlp", e["OTEL_TRACES_EXPORTER"])
	assert.Equal(t, "http/protobuf", e["OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"])
	assert.Equal(t, []string{"cp", "-r", "/autoinstrumentation/.", "/otel-auto-instrumentation-python"}, py.Spec.InitContainers[0].Command)

	j := injectAll(t, basePod(), 0, defJava, "", "", "")
	e = envOf(j.Spec.Containers[0])
	assert.NotContains(t, e, "JAVA_TOOL_OPTIONS")
	assert.Equal(t, "/otel-auto-instrumentation-java/javaagent.jar", e[envInjectorJavaAgentPath])
	assert.Equal(t, "/otel-auto-instrumentation-java/injector/libotelinject.so", e["LD_PRELOAD"])
	assert.Equal(t, []string{"cp", "-r", "/autoinstrumentation/.", "/otel-auto-instrumentation-java"}, j.Spec.InitContainers[0].Command)

	n := injectAll(t, basePod(), 0, "", defNode, "", "")
	e = envOf(n.Spec.Containers[0])
	assert.NotContains(t, e, "NODE_OPTIONS")
	assert.Equal(t, "/otel-auto-instrumentation-nodejs/adot-guard.js", e[envInjectorNodeJSAgentPath])
	assert.Equal(t, "/otel-auto-instrumentation-nodejs/injector/libotelinject.so", e["LD_PRELOAD"])

	d := injectAll(t, basePod(), 0, "", "", "", defDotNet)
	e = envOf(d.Spec.Containers[0])
	for _, k := range []string{"CORECLR_ENABLE_PROFILING", "CORECLR_PROFILER", "CORECLR_PROFILER_PATH", "DOTNET_STARTUP_HOOKS", "DOTNET_ADDITIONAL_DEPS", "DOTNET_SHARED_STORE", "OTEL_DOTNET_AUTO_HOME"} {
		assert.NotContains(t, e, k)
	}
	assert.Equal(t, "/otel-auto-instrumentation-dotnet", e[envInjectorDotNetPathPrefix])
	assert.Equal(t, "/otel-auto-instrumentation-dotnet/injector/libotelinject.so", e["LD_PRELOAD"])
}

func TestInjectorModeAllFourLanguagesOneLDPreload(t *testing.T) {
	setInjectorDefaults(t, InjectionModeInjector)
	p := injectAll(t, basePod(), 0, defJava, defNode, defPython, defDotNet)
	e := envOf(p.Spec.Containers[0])
	assert.Equal(t, "/otel-auto-instrumentation-java/injector/libotelinject.so", e["LD_PRELOAD"])
	n := 0
	for _, env := range p.Spec.Containers[0].Env {
		if env.Name == "LD_PRELOAD" {
			n++
		}
	}
	assert.Equal(t, 1, n)
	for _, k := range []string{envInjectorJavaAgentPath, envInjectorNodeJSAgentPath, envInjectorPythonPathPrefix, envInjectorDotNetPathPrefix} {
		assert.Contains(t, e, k)
	}
	// applying again (webhook re-invocation) does not duplicate
	p = applyInjectorPreload(p)
	assert.Equal(t, "/otel-auto-instrumentation-java/injector/libotelinject.so", envOf(p.Spec.Containers[0])["LD_PRELOAD"])
}

func TestInjectorModeCustomerLDPreloadAppended(t *testing.T) {
	setInjectorDefaults(t, InjectionModeInjector)
	p := injectAll(t, basePod(corev1.Container{Name: "app", Env: []corev1.EnvVar{{Name: "LD_PRELOAD", Value: "/usr/lib/libjemalloc.so"}}}), 0, "", "", defPython, "")
	assert.Equal(t, "/usr/lib/libjemalloc.so:/otel-auto-instrumentation-python/injector/libotelinject.so", envOf(p.Spec.Containers[0])["LD_PRELOAD"])
}

func TestInjectorModeLDPreloadFromValueFromKeepsOldInjection(t *testing.T) {
	setInjectorDefaults(t, InjectionModeInjector)
	c := corev1.Container{Name: "app", Env: []corev1.EnvVar{{Name: "LD_PRELOAD", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "k"}}}}}
	p := injectAll(t, basePod(c), 0, "", "", defPython, "")
	e := envOf(p.Spec.Containers[0])
	assert.Equal(t, pythonPathPrefix+":"+pythonPathSuffix, e["PYTHONPATH"])
	assert.NotContains(t, e, envInjectorPythonPathPrefix)
}

func TestInjectorModeCustomImageKeepsOldInjection(t *testing.T) {
	setInjectorDefaults(t, InjectionModeInjector)
	old := "public.ecr.aws/aws-observability/adot-autoinstrumentation-python:v0.19.0"
	p := injectAll(t, basePod(), 0, "", "", old, "")
	e := envOf(p.Spec.Containers[0])
	assert.Equal(t, pythonPathPrefix+":"+pythonPathSuffix, e["PYTHONPATH"])
	assert.NotContains(t, e, "LD_PRELOAD")
	assert.NotContains(t, e, envInjectorPythonPathPrefix)

	oldJava := "public.ecr.aws/aws-observability/adot-autoinstrumentation-java:v2.10.0"
	j := injectAll(t, basePod(), 0, oldJava, "", "", "")
	assert.Equal(t, javaJVMArgument, envOf(j.Spec.Containers[0])["JAVA_TOOL_OPTIONS"])
	assert.Equal(t, javaCommandLinux, j.Spec.InitContainers[0].Command)
}

func TestInjectorModeMixedCustomAndDefaultImages(t *testing.T) {
	setInjectorDefaults(t, InjectionModeInjector)
	oldPython := "my/python:pinned"
	p := injectAll(t, basePod(), 0, defJava, "", oldPython, "")
	e := envOf(p.Spec.Containers[0])
	assert.Equal(t, pythonPathPrefix+":"+pythonPathSuffix, e["PYTHONPATH"], "custom image: today's injection")
	assert.NotContains(t, e, "JAVA_TOOL_OPTIONS")
	assert.Equal(t, "/otel-auto-instrumentation-java/injector/libotelinject.so", e["LD_PRELOAD"], "LD_PRELOAD from the injector language")

	// only custom images: no LD_PRELOAD at all
	q := injectAll(t, basePod(), 0, "my/java:pinned", "", oldPython, "")
	assert.NotContains(t, envOf(q.Spec.Containers[0]), "LD_PRELOAD")
}

func TestInjectorModeMultiContainer(t *testing.T) {
	setInjectorDefaults(t, InjectionModeInjector)
	p := basePod(corev1.Container{Name: "app"}, corev1.Container{Name: "sidecar"})
	p = injectAll(t, p, 0, "", "", defPython, "")
	assert.Contains(t, envOf(p.Spec.Containers[0]), "LD_PRELOAD")
	assert.NotContains(t, envOf(p.Spec.Containers[1]), "LD_PRELOAD")
	// both containers injected (container-names annotation)
	p2 := basePod(corev1.Container{Name: "app"}, corev1.Container{Name: "sidecar"})
	var err error
	p2, err = injectPythonSDK(v1alpha1.Python{Image: defPython}, p2, 0, nil)
	require.NoError(t, err)
	p2, err = injectPythonSDK(v1alpha1.Python{Image: defPython}, p2, 1, nil)
	require.NoError(t, err)
	p2 = applyInjectorPreload(p2)
	assert.Equal(t, "/otel-auto-instrumentation-python/injector/libotelinject.so", envOf(p2.Spec.Containers[0])["LD_PRELOAD"])
	assert.Equal(t, "/otel-auto-instrumentation-python/injector/libotelinject.so", envOf(p2.Spec.Containers[1])["LD_PRELOAD"])
	assert.Len(t, p2.Spec.InitContainers, 1)
}

func TestOldModeUnchanged(t *testing.T) {
	setInjectorDefaults(t, InjectionModeOld)
	p := injectAll(t, basePod(), 0, defJava, defNode, defPython, defDotNet)
	e := envOf(p.Spec.Containers[0])
	assert.Equal(t, javaJVMArgument, e["JAVA_TOOL_OPTIONS"])
	assert.Equal(t, nodeRequireArgument, e["NODE_OPTIONS"])
	assert.Equal(t, pythonPathPrefix+":"+pythonPathSuffix, e["PYTHONPATH"])
	assert.Equal(t, dotNetCoreClrProfilerGlibcPath, e["CORECLR_PROFILER_PATH"])
	assert.Equal(t, dotNetOTelAutoHomePath, e["OTEL_DOTNET_AUTO_HOME"])
	assert.NotContains(t, e, "LD_PRELOAD")
	for _, k := range []string{envInjectorJavaAgentPath, envInjectorNodeJSAgentPath, envInjectorPythonPathPrefix, envInjectorDotNetPathPrefix} {
		assert.NotContains(t, e, k)
	}
	assert.Equal(t, javaCommandLinux, p.Spec.InitContainers[0].Command)
}

func TestWindowsPodsUnchangedInInjectorMode(t *testing.T) {
	setInjectorDefaults(t, InjectionModeInjector)
	win := func() corev1.Pod {
		p := basePod()
		p.Spec.NodeSelector = map[string]string{"kubernetes.io/os": "windows"}
		return p
	}
	got := injectAll(t, win(), 0, defJava, "", "", defDotNet)
	require.NoError(t, SetInjectionMode(InjectionModeOld))
	want := injectAll(t, win(), 0, defJava, "", "", defDotNet)
	assert.Equal(t, want, got, "Windows pods get exactly today's injection")
	e := envOf(got.Spec.Containers[0])
	assert.Equal(t, dotNetCoreClrProfilerPathWindows, e["CORECLR_PROFILER_PATH"])
	assert.Equal(t, javaJVMArgument, e["JAVA_TOOL_OPTIONS"])
	assert.NotContains(t, e, "LD_PRELOAD")
	assert.Equal(t, javaCommandWindows, got.Spec.InitContainers[0].Command)
}

func TestInjectorModeValidationUnchanged(t *testing.T) {
	setInjectorDefaults(t, InjectionModeInjector)
	// today's operator refuses JAVA_TOOL_OPTIONS from valueFrom: still refused
	c := corev1.Container{Name: "app", Env: []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{Key: "k"}}}}}
	_, err := injectJavaagent(v1alpha1.Java{Image: defJava}, basePod(c), 0, nil)
	assert.Error(t, err)
	// customer JAVA_TOOL_OPTIONS value: left to the injector, which appends -javaagent in the process
	c2 := corev1.Container{Name: "app", Env: []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS", Value: "-Xmx256m"}}}
	p := injectAll(t, basePod(c2), 0, defJava, "", "", "")
	assert.Equal(t, "-Xmx256m", envOf(p.Spec.Containers[0])["JAVA_TOOL_OPTIONS"])
	// OTEL_DOTNET_AUTO_HOME set by the customer: refused as today
	c3 := corev1.Container{Name: "app", Env: []corev1.EnvVar{{Name: "OTEL_DOTNET_AUTO_HOME", Value: "/x"}}}
	_, err = injectDotNetSDK(v1alpha1.DotNet{Image: defDotNet}, basePod(c3), 0, "", c3.Env)
	assert.Error(t, err)
}

func TestInjectionModePerLanguage(t *testing.T) {
	setInjectorDefaults(t, "python")
	// Python through the injector, Java in the same container as today
	p := injectAll(t, basePod(), 0, defJava, "", defPython, "")
	e := envOf(p.Spec.Containers[0])
	assert.NotContains(t, e, "PYTHONPATH")
	assert.Equal(t, "/otel-auto-instrumentation-python", e[envInjectorPythonPathPrefix])
	assert.Equal(t, javaJVMArgument, e["JAVA_TOOL_OPTIONS"], "java not listed: today's injection")
	assert.NotContains(t, e, envInjectorJavaAgentPath)
	assert.Equal(t, "/otel-auto-instrumentation-python/injector/libotelinject.so", e["LD_PRELOAD"])
	assert.Equal(t, javaCommandLinux, p.Spec.InitContainers[0].Command, "java init container as today")

	// Java / Node / .NET pods: exactly today's injection
	require.NoError(t, SetInjectionMode(InjectionModeOld))
	want := injectAll(t, basePod(), 0, defJava, defNode, "", defDotNet)
	require.NoError(t, SetInjectionMode("python"))
	got := injectAll(t, basePod(), 0, defJava, defNode, "", defDotNet)
	assert.Equal(t, want, got)

	require.NoError(t, SetInjectionMode(" Python , nodejs "))
	q := injectAll(t, basePod(), 0, defJava, defNode, defPython, "")
	e = envOf(q.Spec.Containers[0])
	assert.Equal(t, "/otel-auto-instrumentation-nodejs/injector/libotelinject.so", e["LD_PRELOAD"], "first injector language in java,nodejs,python,dotnet order")
	assert.Equal(t, javaJVMArgument, e["JAVA_TOOL_OPTIONS"])
	assert.NotContains(t, e, "NODE_OPTIONS")

	assert.Error(t, SetInjectionMode("python,ruby"))
	assert.Error(t, SetInjectionMode("go"))
}
