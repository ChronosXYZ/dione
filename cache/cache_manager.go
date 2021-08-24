package cache

type CacheManager interface {
	// Cache returns kv cache with specific name
	Cache(name string) Cache
}
