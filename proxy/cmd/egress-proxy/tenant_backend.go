// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

package main

// Where a tenant's policy document lives: in the holder's own Drive folder,
// beside their sessions and workspace, under their keys (D6′). The harness
// keeps an in-memory copy for the life of the process and nothing on its
// volume. Before the holder has connected a Drive, a policy they set applies
// immediately but is reported as not persisted, so the UI can say so instead
// of implying durability the enclave does not have.

import (
	"net/http"

	"github.com/Privasys/attested-harness/proxy/internal/capability"
	"github.com/Privasys/attested-harness/proxy/internal/policy"
)

// tenantPolicyFile is the document's name at the root of the granted folder.
const tenantPolicyFile = "policy.json"

type driveTenantBackend struct {
	broker    *capability.Broker
	client    *http.Client
	driveHost string
}

func newDriveTenantBackend(broker *capability.Broker, client *http.Client, driveHost string) *driveTenantBackend {
	return &driveTenantBackend{broker: broker, client: client, driveHost: driveHost}
}

// store resolves the holder's granted folder, or nil when there is none yet.
func (b *driveTenantBackend) store(sub string) *capability.DriveStore {
	if b.driveHost == "" || !b.broker.Enabled() {
		return nil
	}
	g := b.broker.Granted(sub)
	if !g.Usable() {
		return nil
	}
	ds, err := capability.NewDriveStore(b.client, b.driveHost, b.broker, g)
	if err != nil {
		return nil
	}
	return ds
}

func (b *driveTenantBackend) Load(sub string) ([]byte, bool, error) {
	ds := b.store(sub)
	if ds == nil {
		return nil, false, nil
	}
	n, found, err := ds.ResolvePath(ds.RootID(), tenantPolicyFile)
	if err != nil || !found {
		return nil, false, err
	}
	raw, err := ds.Get(n.ID)
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func (b *driveTenantBackend) Save(sub string, raw []byte) error {
	ds := b.store(sub)
	if ds == nil {
		return policy.ErrNotPersisted
	}
	_, err := ds.Put(tenantPolicyFile, raw)
	return err
}
