package redis

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/Secured-Finance/dione/cache"

	"github.com/go-redis/redis/v8"
)

type Cache struct {
	redisClient *redis.Client
	ctx         context.Context
	name        string
}

func NewCache(redisClient *redis.Client, name string) *Cache {
	return &Cache{
		redisClient: redisClient,
		ctx:         context.Background(),
		name:        name,
	}
}

func (rc *Cache) Store(key string, value interface{}) error {
	data, err := gobMarshal(value)
	if err != nil {
		return err
	}

	rc.redisClient.Set(rc.ctx, rc.name+"/"+key, data, 0)

	return nil
}

func (rc *Cache) StoreWithTTL(key string, value interface{}, ttl time.Duration) error {
	data, err := gobMarshal(value)
	if err != nil {
		return err
	}
	rc.redisClient.Set(rc.ctx, rc.name+"/"+key, data, ttl)

	return nil
}

func gobMarshal(val interface{}) ([]byte, error) {
	buf := bytes.NewBuffer(nil)
	enc := gob.NewEncoder(buf)
	err := enc.Encode(val)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gobUnmarshal(data []byte, val interface{}) error {
	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)
	return dec.Decode(&val)
}

func (rc *Cache) Get(key string, value interface{}) error {
	data, err := rc.redisClient.Get(rc.ctx, rc.name+"/"+key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return cache.ErrNotFound
		}
		return err
	}

	return gobUnmarshal(data, &value)
}

func (rc *Cache) Delete(key string) {
	rc.redisClient.Del(rc.ctx, rc.name+"/"+key)
}

func (rc *Cache) Keys() []string {
	res := rc.redisClient.Keys(rc.ctx, rc.name+"/"+"*")
	if res.Err() != nil {
		logrus.Error(res.Err())
		return nil
	}
	return res.Val()
}

func (rc *Cache) Exists(key string) bool {
	res := rc.redisClient.Exists(rc.ctx, rc.name+"/"+key)
	if res.Err() != nil {
		return false
	}
	if res.Val() == 0 {
		return false
	}
	return true
}
