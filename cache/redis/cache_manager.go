package redis

import (
	"github.com/Secured-Finance/dione/cache"
	"github.com/Secured-Finance/dione/config"
	"github.com/go-redis/redis/v8"
)

type CacheManager struct {
	redisClient *redis.Client
	caches      map[string]*Cache
}

func NewCacheManager(cfg *config.Config) cache.CacheManager {
	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	return &CacheManager{
		redisClient: redisClient,
	}
}

func (cm *CacheManager) Cache(name string) cache.Cache {
	var c *Cache
	if v, ok := cm.caches[name]; !ok {
		c = NewCache(cm.redisClient, name)
		cm.caches[name] = c
	} else {
		c = v
	}
	return c
}
