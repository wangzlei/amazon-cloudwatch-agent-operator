// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package auto

import "github.com/aws/amazon-cloudwatch-agent-operator/pkg/instrumentation"

// AnnotationConfig details the resources that have enabled
// auto-annotation for each instrumentation type.
//
// The same JSON may also carry instrumentationGuard (the customer switch for the OpenTelemetry Injector with the
// ADOT start-up guard). It is deliberately not a field here: it is read by InstrumentationGuardFromConfig, on its own,
// because it applies to every injected pod (also when auto-monitor is disabled), and so that a malformed value cannot
// stop auto-monitor from being configured.
type MonitorConfig struct {
	MonitorAllServices bool                    `json:"monitorAllServices"`
	Languages          instrumentation.TypeSet `json:"languages,omitempty"`
	RestartPods        bool                    `json:"restartPods"`
	Exclude            AnnotationConfig        `json:"exclude,omitempty"`
	CustomSelector     AnnotationConfig        `json:"customSelector,omitempty"`
}
