package api

import (
	"github.com/gastownhall/gascity/internal/api/apierr"
	"golang.org/x/sync/singleflight"
)

// shareListBuild runs build once for every overlapping call with the same
// key. A caller that arrives while a build is running waits for it and shares
// its answer, and each sharer gets its own deep copy, the way response-cache
// hits do, so one caller can never change what another serves.
//
// Use it for a list endpoint that is costly to build and that dashboards ask
// for again on every live event. On the town the copies of /agents and
// /sessions piled up: each took 1.5 to 3s alone and 15s to 4 minutes in a
// crowd, so the phone page never got an answer (hq-subxy4). Pass
// cacheKeyFor(<endpoint>, input) as the key, so only requests with the same
// parameters share.
func shareListBuild[T any](flight *singleflight.Group, key string, build func() (*ListOutput[T], error)) (*ListOutput[T], error) {
	v, err, shared := flight.Do(key, func() (any, error) {
		return build()
	})
	if err != nil {
		return nil, err
	}
	out := v.(*ListOutput[T])
	if !shared {
		return out, nil
	}
	body, ok := cloneCachedValue[ListBody[T]](out.Body)
	if !ok {
		return nil, apierr.Internal.Msg("copying a shared list failed")
	}
	return &ListOutput[T]{Index: out.Index, CacheAgeS: out.CacheAgeS, Body: body}, nil
}
