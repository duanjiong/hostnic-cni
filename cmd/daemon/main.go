//
// =========================================================================
// Copyright (C) 2017 by Yunify, Inc...
// -------------------------------------------------------------------------
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this work except in compliance with the License.
// You may obtain a copy of the License in the LICENSE file, or at:
//
//  http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// =========================================================================
//

package main

import (
	"flag"
	"github.com/davecgh/go-spew/spew"
	log "github.com/sirupsen/logrus"
	"github.com/yunify/hostnic-cni/pkg/db"
	"github.com/yunify/hostnic-cni/pkg/ipam"
	k8sapi "github.com/yunify/hostnic-cni/pkg/k8sclient"
	log2 "github.com/yunify/hostnic-cni/pkg/log"
	"github.com/yunify/hostnic-cni/pkg/networkutils"
	"github.com/yunify/hostnic-cni/pkg/qcclient"
	"github.com/yunify/hostnic-cni/pkg/signals"
	"github.com/yunify/hostnic-cni/pkg/types"
	"os"
)

func main() {
	//parse flag and setup log
	logOpts := log2.NewLogOptions()
	logOpts.AddFlags()

	dbOpts := db.NewLevelDBOptions()
	dbOpts.AddFlags()

	flag.Parse()
	log2.Setup(logOpts)
	db.InitLevelDB(dbOpts)


	// set up signals so we handle the first shutdown signals gracefully
	stopCh := signals.SetupSignalHandler()

	k8sClient := k8sapi.NewK8sHelper(stopCh)
	qcClient := qcclient.NewQingCloudClient()

	conf := ipam.TryLoadFromDisk(types.DefaultConfigName, types.DefaultConfigPath)
	log.Infof("hostnic config is %s", spew.Sdump(conf))

	netlink := networkutils.NetworkUtils{}
	udev := ipam.Udev{}
	pool := ipam.NewPoolManager(qcClient, conf.Pool, udev, netlink)
	pool.Start(stopCh)

	ipamd := ipam.NewIpamD(k8sClient, &conf.Server, pool)
	ipamd.Start(stopCh)

	log.Info("all setup done, wait on stopCh")
	select {
	case <-stopCh:
		log.Info("Daemon exit")
		db.CloseDB()
		os.Exit(0)
	}
}
