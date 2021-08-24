package inmemory

import (
	"fmt"
	"reflect"
	"time"

	"github.com/Secured-Finance/dione/cache"

	cache2 "github.com/patrickmn/go-cache"
)

const (
	DefaultCacheExpiration = 5 * time.Minute
	DefaultGCInterval      = 10 * time.Minute
)

type Cache struct {
	cache *cache2.Cache
}

func NewCache() *Cache {
	return &Cache{
		cache: cache2.New(DefaultCacheExpiration, DefaultGCInterval),
	}
}

func (imc *Cache) Store(key string, value interface{}) error {
	imc.cache.Set(key, value, cache2.NoExpiration)

	return nil
}

func (imc *Cache) StoreWithTTL(key string, value interface{}, ttl time.Duration) error {
	imc.cache.Set(key, value, ttl)
	return nil
}

func (imc *Cache) Get(key string, value interface{}) error {
	v, exists := imc.cache.Get(key)
	if !exists {
		return cache.ErrNotFound
	}
	reflectedValue := reflect.ValueOf(value)
	if reflectedValue.Kind() != reflect.Ptr {
		return fmt.Errorf("value isn't a pointer")
	}
	if reflectedValue.IsNil() {
		reflectedValue.Set(reflect.New(reflectedValue.Type().Elem()))
	}
	reflectedValue.Elem().Set(reflect.ValueOf(v).Elem())

	return nil
}

func (imc *Cache) Delete(key string) {
	imc.cache.Delete(key)
}

func (imc *Cache) Keys() []string {
	var keys []string
	for k := range imc.cache.Items() {
		keys = append(keys, k)
	}
	return keys
}

func (imc *Cache) Exists(key string) bool {
	_, exists := imc.cache.Get(key)
	return exists
}
