package inmemory

import "github.com/Secured-Finance/dione/cache"

type CacheManager struct {
	caches map[string]*Cache
}

func NewCacheManager() cache.CacheManager {
	return &CacheManager{
		caches: map[string]*Cache{},
	}
}

func (cm *CacheManager) Cache(name string) cache.Cache {
	var c *Cache
	if v, ok := cm.caches[name]; !ok {
		c = NewCache()
		cm.caches[name] = c
	} else {
		c = v
	}
	return c
}
