package cache

import (
	"time"

	"github.com/patrickmn/go-cache"
)

const (
	DefaultCacheExpiration = 5 * time.Minute
	DefaultGCInterval      = 10 * time.Minute
)

type InMemoryCache struct {
	cache *cache.Cache
}

func NewInMemoryCache() *InMemoryCache {
	return &InMemoryCache{
		cache: cache.New(DefaultCacheExpiration, DefaultGCInterval),
	}
}

func (imc *InMemoryCache) Store(key string, value interface{}) error {
	imc.cache.Set(key, value, cache.NoExpiration)

	return nil
}

func (imc *InMemoryCache) StoreWithTTL(key string, value interface{}, ttl time.Duration) error {
	imc.cache.Set(key, value, ttl)
	return nil
}

func (imc *InMemoryCache) Get(key string, value interface{}) error {
	v, exists := imc.cache.Get(key)
	if !exists {
		return ErrNilValue
	}
	value = v

	return nil
}

func (imc *InMemoryCache) Delete(key string) {
	imc.cache.Delete(key)
}
