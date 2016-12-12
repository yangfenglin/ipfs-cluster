package ipfscluster

import (
	"context"
	"sync"

	cid "gx/ipfs/QmcTcsTvfaeEBRFo1TkFgT8sRmgi1n1LTZpecfVP8fzpGD/go-cid"
)

const (
	pinEverywhere = -1
)

const (
	PinError = iota
	UnpinError
	Pinned
	Pinning
	Unpinning
	Unpinned
)

type Pin struct {
	Cid     *cid.Cid
	PinMode PinMode
	Status  PinStatus
}

type PinMode int
type PinStatus int

type MapPinTracker struct {
	mux    sync.Mutex
	status map[string]Pin
	rpcCh  chan ClusterRPC

	shutdownCh chan struct{}
	doneCh     chan struct{}

	ctx context.Context
}

func NewMapPinTracker() *MapPinTracker {
	ctx := context.Background()
	mpt := &MapPinTracker{
		status:     make(map[string]Pin),
		rpcCh:      make(chan ClusterRPC, RPCMaxQueue),
		shutdownCh: make(chan struct{}),
		doneCh:     make(chan struct{}),
		ctx:        ctx,
	}
	go mpt.run()
	return mpt
}

func (mpt *MapPinTracker) run() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mpt.ctx = ctx
	for {
		select {
		case <-mpt.shutdownCh:
			close(mpt.doneCh)
			return
		}
	}
	// Great plans for this thread
}

func (mpt *MapPinTracker) set(c *cid.Cid, s PinStatus) error {
	mpt.mux.Lock()
	defer mpt.mux.Unlock()
	if s == Unpinned {
		delete(mpt.status, c.String())
		return nil
	}

	mpt.status[c.String()] = Pin{
		Cid:     c,
		PinMode: pinEverywhere,
		Status:  s,
	}
	return nil
}

func (mpt *MapPinTracker) get(c *cid.Cid) Pin {
	mpt.mux.Lock()
	defer mpt.mux.Unlock()
	p, ok := mpt.status[c.String()]
	if !ok {
		return Pin{
			Cid:    c,
			Status: Unpinned,
		}
	}
	return p
}

func (mpt *MapPinTracker) Pinning(c *cid.Cid) error {
	return mpt.set(c, Pinning)
}

func (mpt *MapPinTracker) Unpinning(c *cid.Cid) error {
	return mpt.set(c, Unpinning)
}

func (mpt *MapPinTracker) Pinned(c *cid.Cid) error {
	return mpt.set(c, Pinned)
}

func (mpt *MapPinTracker) PinError(c *cid.Cid) error {
	return mpt.set(c, PinError)
}

func (mpt *MapPinTracker) UnpinError(c *cid.Cid) error {
	return mpt.set(c, UnpinError)
}

func (mpt *MapPinTracker) Unpinned(c *cid.Cid) error {
	return mpt.set(c, Unpinned)
}

func (mpt *MapPinTracker) GetPin(c *cid.Cid) Pin {
	return mpt.get(c)
}

func (mpt *MapPinTracker) ListPins() []Pin {
	mpt.mux.Lock()
	defer mpt.mux.Unlock()
	pins := make([]Pin, 0, len(mpt.status))
	for _, v := range mpt.status {
		pins = append(pins, v)
	}
	return pins
}

func (mpt *MapPinTracker) Sync(c *cid.Cid) bool {
	ctx, cancel := context.WithCancel(mpt.ctx)
	defer cancel()

	p := mpt.get(c)

	if p.Status == PinError || p.Status == UnpinError {
		return false
	}

	resp := MakeRPC(ctx, mpt.rpcCh, RPC(IPFSIsPinnedRPC, c), true)
	if resp.Error != nil {
		if p.Status == Pinned || p.Status == Pinning {
			mpt.set(c, PinError)
			return true
		}
		if p.Status == Unpinned || p.Status == Unpinning {
			mpt.set(c, UnpinError)
			return true
		}
		return false
	}
	ipfsPinned, ok := resp.Data.(bool)
	if !ok {
		logger.Error("wrong type of IPFSIsPinnedRPC response")
		return false
	}

	if ipfsPinned {
		switch p.Status {
		case Pinned:
			return false
		case Pinning:
			mpt.set(c, Pinned)
			return true
		case Unpinning:
			mpt.set(c, UnpinError) // Not sure here
			return true
		case Unpinned:
			mpt.set(c, UnpinError)
			return true
		}
	} else {
		switch p.Status {
		case Pinned:
			mpt.set(c, PinError)
			return true
		case Pinning:
			mpt.set(c, PinError)
			return true
		case Unpinning:
			mpt.set(c, Unpinned)
			return true
		case Unpinned:
			return false
		}
	}
	return false
}

func (mpt *MapPinTracker) Recover(c *cid.Cid) error {
	p := mpt.get(c)
	if p.Status != PinError && p.Status != UnpinError {
		return nil
	}

	ctx, cancel := context.WithCancel(mpt.ctx)
	defer cancel()
	if p.Status == PinError {
		MakeRPC(ctx, mpt.rpcCh, RPC(IPFSPinRPC, c), false)
	}
	if p.Status == UnpinError {
		MakeRPC(ctx, mpt.rpcCh, RPC(IPFSUnpinRPC, c), false)
	}
	return nil
}

func (mpt *MapPinTracker) SyncAll() []Pin {
	var changedPins []Pin
	pins := mpt.ListPins()
	for _, p := range pins {
		changed := mpt.Sync(p.Cid)
		if changed {
			changedPins = append(changedPins, p)
		}
	}
	return changedPins
}

func (mpt *MapPinTracker) SyncState(cState ClusterState) []Pin {
	clusterPins := cState.ListPins()
	clusterMap := make(map[string]struct{})
	// Make a map for faster lookup
	for _, c := range clusterPins {
		var a struct{}
		clusterMap[c.String()] = a
	}
	var toRemove []*cid.Cid
	var toAdd []*cid.Cid
	var changed []Pin
	mpt.mux.Lock()

	// Collect items in the ClusterState not in the tracker
	for _, c := range clusterPins {
		_, ok := mpt.status[c.String()]
		if !ok {
			toAdd = append(toAdd, c)
		}
	}

	// Collect items in the tracker not in the ClusterState
	for _, p := range mpt.status {
		_, ok := clusterMap[p.Cid.String()]
		if !ok {
			toRemove = append(toRemove, p.Cid)
		}
	}

	// Update new items and mark them as pinning error
	for _, c := range toAdd {
		p := Pin{
			Cid:     c,
			PinMode: pinEverywhere,
			Status:  PinError,
		}
		mpt.status[c.String()] = p
		changed = append(changed, p)
	}

	// Mark items that need to be removed as unpin error
	for _, c := range toRemove {
		p := Pin{
			Cid:     c,
			PinMode: pinEverywhere,
			Status:  UnpinError,
		}
		mpt.status[c.String()] = p
		changed = append(changed, p)
	}
	mpt.mux.Unlock()
	return changed
}

func (mpt *MapPinTracker) Shutdown() error {
	logger.Info("Stopping MapPinTracker")
	close(mpt.shutdownCh)
	<-mpt.doneCh
	return nil
}

func (mpt *MapPinTracker) RpcChan() <-chan ClusterRPC {
	return mpt.rpcCh
}
