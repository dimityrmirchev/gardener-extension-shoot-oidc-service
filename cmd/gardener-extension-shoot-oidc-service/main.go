// SPDX-FileCopyrightText: 2021 SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"

	"github.com/gardener/gardener-extension-shoot-oidc-service/cmd/gardener-extension-shoot-oidc-service/app"
	"github.com/gardener/gardener/pkg/logger"

	runtimelog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
)

func main() {
	runtimelog.SetLogger(logger.ZapLogger(false))

	ctx := signals.SetupSignalHandler()
	if err := app.NewServiceControllerCommand().ExecuteContext(ctx); err != nil {
		runtimelog.Log.Error(err, "error executing the main controller command")
		os.Exit(1)
	}
}
