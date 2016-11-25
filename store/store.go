package store

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sync"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/libkv"
	kv "github.com/docker/libkv/store"
	"github.com/docker/libkv/store/consul"
	"github.com/docker/libkv/store/etcd"
	"github.com/luizbafilho/fusis/api/types"
	"github.com/luizbafilho/fusis/config"
	"github.com/luizbafilho/fusis/util"
	"github.com/pkg/errors"
)

func init() {
	registryStores()
}

func registryStores() {
	libkv.AddStore(kv.CONSUL, consul.New)
	libkv.AddStore(kv.ETCD, etcd.New)
}

type Store interface {
	AddService(svc *types.Service) error
	DeleteService(svc *types.Service) error
	AddDestination(svc *types.Service, dst *types.Destination) error
	DeleteDestination(svc *types.Service, dst *types.Destination) error

	WatchServices()
	SubscribeServices(ch chan []types.Service)

	WatchDestinations()
	SubscribeDestinations(ch chan []types.Destination)

	AddCheck(check types.CheckSpec) error
	SubscribeChecks(ch chan []types.CheckSpec)

	GetKV() kv.Store
}

var (
	ErrUnsupportedStore = errors.New("[store] Unsupported store")
)

type FusisStore struct {
	sync.Mutex
	kv kv.Store

	servicesChannels    []chan []types.Service
	destinationChannels []chan []types.Destination
	checksChannels      []chan []types.CheckSpec
}

func New(config *config.BalancerConfig) (Store, error) {
	u, err := url.Parse(config.StoreAddress)
	if err != nil {
		return nil, errors.Wrap(err, "error paring store address")
	}

	scheme := u.Scheme
	if scheme != "consul" && scheme != "etcd" {
		return nil, ErrUnsupportedStore
	}

	kv, err := libkv.NewStore(
		kv.Backend(scheme),
		[]string{u.Host},
		nil,
	)
	if err != nil {
		return nil, errors.Wrap(err, "Cannot create store consul")
	}

	svcsChs := []chan []types.Service{}
	dstsChs := []chan []types.Destination{}
	checksChs := []chan []types.CheckSpec{}

	fusisStore := &FusisStore{
		kv:                  kv,
		servicesChannels:    svcsChs,
		destinationChannels: dstsChs,
		checksChannels:      checksChs,
	}

	go fusisStore.WatchServices()
	go fusisStore.WatchDestinations()

	return fusisStore, nil
}

func (s *FusisStore) GetKV() kv.Store {
	return s.kv
}

func (s *FusisStore) AddService(svc *types.Service) error {
	key := fmt.Sprintf("fusis/services/%s/config", svc.GetId())

	value, err := json.Marshal(svc)
	if err != nil {
		return errors.Wrapf(err, "error marshaling service: %v", svc)
	}

	err = s.kv.Put(key, value, nil)
	if err != nil {
		return errors.Wrapf(err, "error sending service to store: %v", svc)
	}

	return nil
}

func (s *FusisStore) DeleteService(svc *types.Service) error {
	key := fmt.Sprintf("fusis/services/%s", svc.GetId())

	err := s.kv.DeleteTree(key)
	if err != nil {
		return errors.Wrapf(err, "error trying to delete service: %v", svc)
	}

	return nil
}

func (s *FusisStore) SubscribeServices(updateCh chan []types.Service) {
	s.Lock()
	defer s.Unlock()
	s.servicesChannels = append(s.servicesChannels, updateCh)
}

func (s *FusisStore) WatchServices() {
	svcs := []types.Service{}

	stopCh := make(<-chan struct{})
	events, err := s.kv.WatchTree("fusis/services", stopCh)
	if err != nil {
		log.Error("[store] ", err)
	}

	for {
		select {
		case entries := <-events:
			log.Debug("[store] Services received")

			for _, pair := range entries {
				svc := types.Service{}
				if err := json.Unmarshal(pair.Value, &svc); err != nil {
					log.Error("[store] ", err)
				}
				log.Debugf("[store] Got sevice: %#v ", svc)

				svcs = append(svcs, svc)
			}

			s.Lock()
			for _, ch := range s.servicesChannels {
				ch <- svcs
			}
			s.Unlock()

			//Cleaning up services slice
			svcs = []types.Service{}
		}
	}
}

func (s *FusisStore) AddDestination(svc *types.Service, dst *types.Destination) error {
	key := fmt.Sprintf("fusis/destinations/%s/%s", svc.GetId(), dst.GetId())

	value, err := json.Marshal(dst)
	if err != nil {
		return errors.Wrapf(err, "error marshaling destination: %v", dst)
	}

	err = s.kv.Put(key, value, nil)
	if err != nil {
		return errors.Wrapf(err, "error sending destination to store: %v", dst)
	}
	log.Debugf("[store] Added destination: %#v with key: %s", value, key)

	return nil
}

func (s *FusisStore) DeleteDestination(svc *types.Service, dst *types.Destination) error {
	key := fmt.Sprintf("fusis/destinations/%s/%s", svc.GetId(), dst.GetId())

	err := s.kv.DeleteTree(key)
	if err != nil {
		return errors.Wrapf(err, "error trying to delete destination: %v", dst)
	}
	log.Debugf("[store] Deleted destination: %s", key)

	return nil
}

func (s *FusisStore) SubscribeDestinations(updateCh chan []types.Destination) {
	s.Lock()
	defer s.Unlock()
	s.destinationChannels = append(s.destinationChannels, updateCh)
}

func (s *FusisStore) WatchDestinations() {
	dsts := []types.Destination{}

	stopCh := make(<-chan struct{})
	events, err := s.kv.WatchTree("fusis/destinations", stopCh)
	if err != nil {
		errors.Wrap(err, "failed watching fusis/destinations")
	}

	for {
		select {
		case entries := <-events:
			log.Debug("[store] Destinations received")

			for _, pair := range entries {
				dst := types.Destination{}
				if err := json.Unmarshal(pair.Value, &dst); err != nil {
					errors.Wrap(err, "failed unmarshall of destinations")
				}
				log.Debugf("[store] Got destination: %#v", dst)

				dsts = append(dsts, dst)
			}

			s.Lock()
			for _, ch := range s.destinationChannels {
				ch <- dsts
			}
			s.Unlock()

			//Cleaning up destinations slice
			dsts = []types.Destination{}
		}
	}
}

func (s *FusisStore) SubscribeChecks(updateCh chan []types.CheckSpec) {
	s.Lock()
	defer s.Unlock()
	s.checksChannels = append(s.checksChannels, updateCh)
}

func (s *FusisStore) AddCheck(spec types.CheckSpec) error {
	id := util.RandStr()
	key := fmt.Sprintf("fusis/checks/%s/%s", spec.ServiceID, id)

	value, err := json.Marshal(spec)
	if err != nil {
		return errors.Wrapf(err, "error marshaling CheckSpec: %v", spec)
	}

	err = s.kv.Put(key, value, nil)
	if err != nil {
		return errors.Wrapf(err, "error sending CheckSpec to store: %v", spec)
	}

	return nil
}
