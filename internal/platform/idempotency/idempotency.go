package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrConflict = errors.New("idempotency conflict")
var ErrInProgress = errors.New("idempotency in progress")

type Result struct {
	StatusCode int
	Body       json.RawMessage
	Replayed   bool
}

type Store struct {
	rdb *redis.Client
	ttl time.Duration
}

func New(rdb *redis.Client) *Store {
	return &Store{rdb: rdb, ttl: 24 * time.Hour}
}

func Hash(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Do executes fn once per key. Concurrent callers wait briefly for the first result.
func (s *Store) Do(ctx context.Context, scope, key, requestHash string, fn func(context.Context) (int, any, error)) (Result, error) {
	if s.rdb == nil || key == "" {
		status, body, err := fn(ctx)
		if err != nil {
			return Result{}, err
		}
		raw, _ := json.Marshal(body)
		return Result{StatusCode: status, Body: raw}, nil
	}
	base := fmt.Sprintf("cindy:idem:%s:%s", scope, key)
	lockKey := base + ":lock"
	resultKey := base + ":result"

	ok, err := s.rdb.SetNX(ctx, lockKey, requestHash, 8*time.Second).Result()
	if err != nil {
		return Result{}, err
	}
	if !ok {
		storedHash, _ := s.rdb.Get(ctx, lockKey).Result()
		if storedHash != "" && storedHash != requestHash {
			return Result{}, ErrConflict
		}
		for i := 0; i < 40; i++ {
			raw, err := s.rdb.Get(ctx, resultKey).Result()
			if err == nil && raw != "" {
				var r Result
				if err := json.Unmarshal([]byte(raw), &r); err == nil {
					r.Replayed = true
					return r, nil
				}
			}
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
		return Result{}, ErrInProgress
	}

	status, body, err := fn(ctx)
	if err != nil {
		s.rdb.Del(ctx, lockKey)
		return Result{}, err
	}
	raw, _ := json.Marshal(body)
	res := Result{StatusCode: status, Body: raw}
	encoded, _ := json.Marshal(res)
	s.rdb.Set(ctx, resultKey, encoded, s.ttl)
	s.rdb.Del(ctx, lockKey)
	return res, nil
}
