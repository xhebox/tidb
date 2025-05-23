// Copyright 2023 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package workerpool

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pingcap/tidb/pkg/resourcemanager/util"
	"github.com/pingcap/tidb/pkg/util/logutil"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

var globalCnt atomic.Int64
var cntWg sync.WaitGroup

type int64Task int64

func (int64Task) RecoverArgs() (string, string, func(), bool) {
	return "", "", nil, false
}

type MyWorker[T int64Task, R struct{}] struct {
	id int
}

func (w *MyWorker[T, R]) HandleTask(task int64Task, _ func(struct{})) {
	globalCnt.Add(int64(task))
	cntWg.Done()
	logutil.BgLogger().Info("Worker handling task")
}

func (w *MyWorker[T, R]) Close() {
	logutil.BgLogger().Info("Close worker", zap.Int("id", w.id))
}

func createMyWorker() Worker[int64Task, struct{}] {
	return &MyWorker[int64Task, struct{}]{}
}

func TestWorkerPool(t *testing.T) {
	// Create a worker pool with 3 workers.
	pool := NewWorkerPool[int64Task]("test", util.UNKNOWN, 3, createMyWorker)
	pool.Start(context.Background())
	globalCnt.Store(0)

	g := new(errgroup.Group)
	resultCh := pool.GetResultChan()
	g.Go(func() error {
		// Consume the results.
		for range resultCh {
			// Do nothing.
		}
		return nil
	})
	defer g.Wait()

	// Add some tasks to the pool.
	cntWg.Add(10)
	for i := range 10 {
		pool.AddTask(int64Task(i))
	}

	cntWg.Wait()
	require.Equal(t, int32(3), pool.Cap())
	require.Equal(t, int64(45), globalCnt.Load())

	// Enlarge the pool to 5 workers.
	pool.Tune(5, false)

	// Add some more tasks to the pool.
	cntWg.Add(10)
	for i := range 10 {
		pool.AddTask(int64Task(i))
	}

	cntWg.Wait()
	require.Equal(t, int32(5), pool.Cap())
	require.Equal(t, int64(90), globalCnt.Load())

	// Decrease the pool to 2 workers.
	pool.Tune(2, false)

	// Add some more tasks to the pool.
	cntWg.Add(10)
	for i := range 10 {
		pool.AddTask(int64Task(i))
	}

	cntWg.Wait()
	require.Equal(t, int32(2), pool.Cap())
	require.Equal(t, int64(135), globalCnt.Load())

	// Wait for the tasks to be completed.
	pool.ReleaseAndWait()
}

func TestTunePoolSize(t *testing.T) {
	t.Run("random tune pool size", func(t *testing.T) {
		pool := NewWorkerPool[int64Task]("test", util.UNKNOWN, 3, createMyWorker)
		pool.Start(context.Background())
		seed := time.Now().UnixNano()
		rnd := rand.New(rand.NewSource(seed))
		t.Logf("seed: %d", seed)
		for range 100 {
			wait := rnd.Intn(2) == 0
			larger := pool.Cap() + rnd.Int31n(10) + 2
			pool.Tune(larger, wait)
			require.Equal(t, larger, pool.Cap())
			smaller := pool.Cap() / 2
			pool.Tune(smaller, wait)
			require.Equal(t, smaller, pool.Cap())
		}
		pool.Release()
		pool.Wait()
	})

	t.Run("change pool size before start", func(t *testing.T) {
		pool := NewWorkerPool[int64Task]("test", util.UNKNOWN, 10, createMyWorker)
		pool.Tune(5, true)
		pool.Start(context.Background())
		pool.Release()
		pool.Wait()
		require.EqualValues(t, 5, pool.Cap())
	})

	t.Run("context done when reduce pool size and wait", func(t *testing.T) {
		pool := NewWorkerPool[int64Task]("test", util.UNKNOWN, 10, createMyWorker)
		pool.Start(context.Background())
		pool.Release()
		pool.Tune(5, true)
		pool.Wait()
	})
}

type dummyWorker[T, R any] struct {
}

func (d dummyWorker[T, R]) HandleTask(task T, send func(R)) {
	var r R
	send(r)
}

func (d dummyWorker[T, R]) Close() {}

func TestWorkerPoolNoneResult(t *testing.T) {
	pool := NewWorkerPool[int64Task, None](
		"test", util.UNKNOWN, 3,
		func() Worker[int64Task, None] {
			return dummyWorker[int64Task, None]{}
		})
	pool.Start(context.Background())
	ch := pool.GetResultChan()
	require.Nil(t, ch)
	pool.ReleaseAndWait()

	pool2 := NewWorkerPool[int64Task, int64](
		"test", util.UNKNOWN, 3,
		func() Worker[int64Task, int64] {
			return dummyWorker[int64Task, int64]{}
		})
	pool2.Start(context.Background())
	require.NotNil(t, pool2.GetResultChan())
	pool2.ReleaseAndWait()

	pool3 := NewWorkerPool[int64Task, struct{}](
		"test", util.UNKNOWN, 3,
		func() Worker[int64Task, struct{}] {
			return dummyWorker[int64Task, struct{}]{}
		})
	pool3.Start(context.Background())
	require.NotNil(t, pool3.GetResultChan())
	pool3.ReleaseAndWait()
}

func TestWorkerPoolCustomChan(t *testing.T) {
	pool := NewWorkerPool[int64Task, int64](
		"test", util.UNKNOWN, 3,
		func() Worker[int64Task, int64] {
			return dummyWorker[int64Task, int64]{}
		})

	taskCh := make(chan int64Task)
	pool.SetTaskReceiver(taskCh)
	resultCh := make(chan int64)
	pool.SetResultSender(resultCh)
	count := 0
	g := errgroup.Group{}
	g.Go(func() error {
		for range resultCh {
			count++
		}
		return nil
	})

	pool.Start(context.Background())
	for i := range 5 {
		taskCh <- int64Task(i)
	}
	close(taskCh)
	pool.Wait()
	pool.Release()
	require.NoError(t, g.Wait())
	require.Equal(t, 5, count)
}

func TestWorkerPoolCancelContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pool := NewWorkerPool[int64Task, int64](
		"test", util.UNKNOWN, 3,
		func() Worker[int64Task, int64] {
			return dummyWorker[int64Task, int64]{}
		})
	pool.Start(ctx)
	pool.AddTask(1)

	cancel()
	pool.Wait() // Should not be blocked by the result channel.
	require.Equal(t, 0, int(pool.Running()))
}
