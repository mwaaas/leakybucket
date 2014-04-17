package leakybucket

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

// CreateTest returns a test of bucket creation for a given storage backend.
// It is meant to be used by leakybucket implementers who wish to test this.
func CreateTest(s Storage) func(*testing.T) {
	return func(t *testing.T) {
		now := time.Now()
		bucket, err := s.Create("testbucket", 100, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if capacity := bucket.Capacity(); capacity != 100 {
			t.Fatalf("expected capacity of %d, got %d", 100, capacity)
		}
		e := float64(1 * time.Second) // margin of error
		if error := float64(bucket.Reset().Sub(now.Add(time.Minute))); math.Abs(error) > e {
			t.Fatalf("expected reset time close to %s, got %s", now.Add(time.Minute),
				bucket.Reset())
		}
	}
}

// AddTest returns a test that adding to a single bucket works.
// It is meant to be used by leakybucket implementers who wish to test this.
func AddTest(s Storage) func(*testing.T) {
	return func(t *testing.T) {
		bucket, err := s.Create("testbucket", 10, time.Minute)
		if err != nil {
			t.Fatal(err)
		}

		addAndTestRemaining := func(add, remaining uint) {
			if state, err := bucket.Add(add); err != nil {
				t.Fatal(err)
			} else if bucket.Remaining() != state.Remaining {
				t.Fatalf("expected bucket and state remaining to match, bucket is %d, state is %d",
					bucket.Remaining(), state.Remaining)
			} else if state.Remaining != remaining {
				t.Fatalf("expected %d remaining, got %d", remaining, state.Remaining)
			}
		}

		addAndTestRemaining(1, 9)
		addAndTestRemaining(3, 6)
		addAndTestRemaining(6, 0)

		if _, err := bucket.Add(1); err == nil {
			t.Fatalf("expected ErrorFull, received no error")
		} else if err != ErrorFull {
			t.Fatalf("expected ErrorFull, received %v", err)
		}
	}
}

// AddResetTest returns a test that Add performs properly across reset time boundaries.
// It is meant to be used by leakybucket implementers who wish to test this.
func AddResetTest(s Storage) func(*testing.T) {
	return func(t *testing.T) {
		bucket, err := s.Create("testbucket", 1, time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bucket.Add(1); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond * 2)
		if state, err := bucket.Add(1); err != nil {
			t.Fatal(err)
		} else if state.Remaining != 0 {
			t.Fatalf("expected full bucket, got %d", state.Remaining)
		} else if state.Reset.Unix() < time.Now().Unix() {
			t.Fatalf("reset time is in the past")
		}
	}
}

func compareBucketTimes(a, b Bucket) error {
	if a.Reset().Unix() == b.Reset().Unix() {
		return nil
	}
	return errors.New(fmt.Sprintf("first has %#v reset, second has %#v reset", a.Reset().Unix(), b.Reset().Unix()))
}
func compareBuckets(a, b Bucket) error {
	if a.Remaining() != b.Remaining() {
		return errors.New(fmt.Sprintf("first has %d remaining, second has %d remaining", a.Remaining(), b.Remaining()))
	}
	return compareBucketTimes(a, b)
}

// FindOrCreateTest returns a test that the Create function is essentially a FindOrCreate: if you
// create one bucket, wait some time, and create another bucket with the same name, all the
// properties should be the same.
// It is meant to be used by leakybucket implementers who wish to test this.
func FindOrCreateTest(s Storage) func(*testing.T) {
	return func(t *testing.T) {
		bucket1, err := s.Create("testbucket", 10, time.Minute)
		if err != nil {
			t.Fatal(err)
		}

		// Some leakybucket implementations don't start the TTL until Add is called.
		if _, err := bucket1.Add(1); err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Second * 2)

		bucket2, err := s.Create("testbucket", 10, time.Second)
		if err != nil {
			t.Fatal(err)
		}

		err = compareBuckets(bucket1, bucket2)
		if err != nil {
			t.Fatal(err)
		}
	}
}

// ThreadSafeAddTest returns a test that adding to a single bucket is thread-safe.
// It is meant to be used by leakybucket implementers who wish to test this.
func ThreadSafeAddTest(s Storage) func(*testing.T) {
	return func(t *testing.T) {
		// Make a bucket of size `n`. Spawn `n+1` goroutines that each try to take one token.
		// We should see the bucket transition through having `n-1`, `n-2`, ... 0 remaining capacity.
		// We should also witness one error when the bucket has reached capacity.
		n := 100
		bucket, err := s.Create("testbucket", uint(n), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		remaining := map[uint]bool{}     // record observed "remaining" counts. (ab)using map as set here
		remainingMutex := sync.RWMutex{} // maps are not threadsafe
		errors := []error{}              // record observed errors
		var wg sync.WaitGroup
		for i := 0; i < n+1; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				state, err := bucket.Add(1)
				if err != nil {
					errors = append(errors, err)
				} else {
					remainingMutex.Lock()
					defer remainingMutex.Unlock()
					remaining[state.Remaining] = true
				}
			}()
		}
		wg.Wait()
		if len(remaining) != n {
			keys := []uint{}
			for key := range remaining {
				keys = append(keys, key)
			}
			t.Fatalf("Did not observe correct bucket states. Saw %d distinct remaining values instead of %d: %v",
				len(remaining), n, keys)
		}
		if !(len(errors) == 1 && errors[0] == ErrorFull) {
			t.Fatalf("Did not observe one full error: %#v", errors)
		}
	}
}

// BucketInstanceConsistencyTest returns a test that two instances of a leakybucket pointing to the
// same remote bucket keep consistent state with the remote.
func BucketInstanceConsistencyTest(s Storage) func(*testing.T) {
	return func(t *testing.T) {
		// Create two bucket instances pointing to the same remote bucket
		bucket1, err := s.Create("testbucket", 5, time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		bucket2, err := s.Create("testbucket", 5, time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		// Fill up the remote bucket via the first instance and verify that the second instance
		// becomes full.
		_, err = bucket1.Add(5)
		if err != nil {
			t.Fatal(err)
		}
		_, err = bucket2.Add(1)
		if err == nil {
			t.Fatal("expected an error")
		}
		if err != ErrorFull {
			t.Fatalf("expected ErrorFull, received %#v", err)
		}
		time.Sleep(time.Second * 2)
		// Wait for the bucket to empty and confirm that you can now add via both instances.
		_, err = bucket2.Add(1)
		if err != nil {
			t.Fatal(err)
		}

		_, err = bucket1.Add(1)
		if err != nil {
			t.Fatal(err)
		}

		err = compareBucketTimes(bucket1, bucket2)
		if err != nil {
			t.Fatal(err)
		}

		// Wait for the bucket to empty and confirm that if we fill it up the bucket via one
		// instance, then try to add to the second instance, it has the right reset time.
		time.Sleep(time.Second * 2)

		_, err = bucket1.Add(5)
		if err != nil {
			t.Fatal(err)
		}

		_, err = bucket2.Add(1)
		if err == nil {
			t.Fatal("expected an error")
		}

		err = compareBucketTimes(bucket1, bucket2)
		if err != nil {
			t.Fatal(err)
		}
	}
}
