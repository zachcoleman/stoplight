package main_test

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	m "github.com/zachcoleman/mutex-service"
)

func RunLocalhost() chan struct{} {
	mux := http.NewServeMux()
	mmut := m.MapMutex{
		Keys:  make(map[string]struct{}),
		RKeys: make(map[string]uint),
		Mut:   sync.RWMutex{},
	}
	mux.HandleFunc("GET /health", m.HealthHanderFactory())
	mux.HandleFunc("GET /lock", m.LockHandlerFactory(&mmut))
	mux.HandleFunc("GET /unlock", m.UnlockHandlerFactory(&mmut))
	mux.HandleFunc("GET /rlock", m.RLockHandlerFactory(&mmut))
	mux.HandleFunc("GET /runlock", m.RUnlockHandlerFactory(&mmut))
	mux.HandleFunc("GET /status", m.StatusHandlerFactory(&mmut))
	handler := m.ApplyMiddlewares(mux)
	server := http.Server{
		Addr:    ":8080",
		Handler: handler,
	}
	log.Println("Starting server...")
	go server.ListenAndServe()
	killCh := make(chan struct{})
	go func() {
		<-killCh
		log.Println("Closing server...")
		server.Close()
	}()
	return killCh
}

func TestSerial(t *testing.T) {
	tests := map[string]struct {
		input    []string
		expected []int
	}{
		"lock-unlock-good": {
			input:    []string{"lock?key=blah", "unlock?key=blah", "status?key=blah"},
			expected: []int{http.StatusAccepted, http.StatusAccepted, http.StatusOK},
		},
		"rlock-lock-runlock-lock": {
			input:    []string{"rlock?key=blah", "lock?key=blah", "runlock?key=blah", "lock?key=blah"},
			expected: []int{http.StatusAccepted, http.StatusConflict, http.StatusAccepted, http.StatusAccepted},
		},
		"rlock-status-runlock-status": {
			input:    []string{"rlock?key=blah", "status?key=blah", "runlock?key=blah", "status?key=blah"},
			expected: []int{http.StatusAccepted, http.StatusOK, http.StatusAccepted, http.StatusOK},
		},
		"lock-rlock-unlock-rlock": {
			input:    []string{"lock?key=blah", "rlock?key=blah", "unlock?key=blah", "rlock?key=blah"},
			expected: []int{http.StatusAccepted, http.StatusConflict, http.StatusAccepted, http.StatusAccepted},
		},
		"lock-status-lock": {
			input:    []string{"lock?key=blah", "status?key=blah", "lock?key=blah"},
			expected: []int{http.StatusAccepted, http.StatusLocked, http.StatusConflict},
		},
		"unlock": {
			input:    []string{"unlock?key=blah"},
			expected: []int{http.StatusConflict},
		},
		"status-lock-status-unlock-status": {
			input:    []string{"status?key=blah", "lock?key=blah", "status?key=blah", "unlock?key=blah", "status?key=blah"},
			expected: []int{http.StatusOK, http.StatusAccepted, http.StatusLocked, http.StatusAccepted, http.StatusOK},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			k := RunLocalhost()
			client := http.Client{}
			resp := []int{}
			for _, p := range tt.input {
				r, err := client.Get(fmt.Sprintf("http://localhost:8080/%s", p))
				if err != nil {
					t.Error(err)
				}
				resp = append(resp, r.StatusCode)
			}
			for ix, expected := range tt.expected {
				if resp[ix] != expected {
					t.Errorf("Got %v instead of %v", resp, tt.expected)
				}
			}
			k <- struct{}{}
			time.Sleep(10 * time.Millisecond)
		})
	}
}

func BenchmarkRandom(b *testing.B) {
	k := RunLocalhost()
	client := http.Client{}
	ch := make(chan string, 10)
	randKeys := make([]string, 200)
	for ix := range randKeys {
		randKeys[ix] = strconv.Itoa(rand.Int())
	}
	locks, unlocks, rlocks, runlocks, statuses := randKeys[:], randKeys[:], randKeys[:], randKeys[:], randKeys[:]
	rand.Shuffle(len(locks), func(i, j int) { locks[i], locks[j] = locks[j], locks[i] })
	rand.Shuffle(len(unlocks), func(i, j int) { unlocks[i], unlocks[j] = unlocks[j], unlocks[i] })
	rand.Shuffle(len(rlocks), func(i, j int) { rlocks[i], rlocks[j] = rlocks[j], rlocks[i] })
	rand.Shuffle(len(runlocks), func(i, j int) { runlocks[i], runlocks[j] = runlocks[j], runlocks[i] })
	rand.Shuffle(len(statuses), func(i, j int) { statuses[i], statuses[j] = statuses[j], statuses[i] })
	go func(in chan string) {
		for i := range randKeys {
			in <- fmt.Sprintf("http://localhost:8080/lock?key=%s", locks[i])
			in <- fmt.Sprintf("http://localhost:8080/status?key=%s", statuses[i])
			in <- fmt.Sprintf("http://localhost:8080/unlock?key=%s", unlocks[i])
			in <- fmt.Sprintf("http://localhost:8080/lock?key=%s", rlocks[i])
			in <- fmt.Sprintf("http://localhost:8080/unlock?key=%s", runlocks[i])
		}
		close(in)
	}(ch)
	b.ResetTimer()
	wg := sync.WaitGroup{}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(in chan string) {
			for p := range ch {
				_, err := client.Get(p)
				if err != nil {
					b.Error(err)
				}
			}
			wg.Done()
		}(ch)
	}
	wg.Wait()
	k <- struct{}{}
}

func BenchmarkConsistentLockStatusUnlock(b *testing.B) {
	k := RunLocalhost()
	client := http.Client{}
	ch := make(chan string, 10)
	randKeys := make([]string, 330)
	for ix := range randKeys {
		randKeys[ix] = strconv.Itoa(rand.Int())
	}
	actions := randKeys[:]
	actions = append(actions, randKeys[:]...)
	actions = append(actions, randKeys[:]...)
	actionMap := map[string]int{}
	go func(in chan string) {
		for _, v := range actions {
			count := actionMap[v]
			switch count {
			case 0:
				in <- fmt.Sprintf("http://localhost:8080/lock?key=%s", v)
			case 1:
				in <- fmt.Sprintf("http://localhost:8080/status?key=%s", v)
			case 2:
				in <- fmt.Sprintf("http://localhost:8080/unlock?key=%s", v)
			default:
				b.Errorf("unreachable")
			}
			actionMap[v] += 1
		}
		close(in)
	}(ch)
	b.ResetTimer()
	wg := sync.WaitGroup{}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(in chan string) {
			for p := range ch {
				_, err := client.Get(p)
				if err != nil {
					b.Error(err)
				}
			}
			wg.Done()
		}(ch)
	}
	wg.Wait()
	k <- struct{}{}
}

func BenchmarkMajorityStatus(b *testing.B) {
	k := RunLocalhost()
	client := http.Client{}
	ch := make(chan string, 10)
	randKeys := make([]string, 50)
	for ix := range randKeys {
		randKeys[ix] = strconv.Itoa(rand.Int())
	}
	actions := []string{}
	for i := 0; i < 20; i++ {
		actions = append(actions, randKeys[:]...)
	}
	actionMap := map[string]int{}
	go func(in chan string) {
		for _, v := range actions {
			count := actionMap[v]
			switch {
			case count < 5:
				in <- fmt.Sprintf("http://localhost:8080/status?key=%s", v)
			case count == 5:
				in <- fmt.Sprintf("http://localhost:8080/lock?key=%s", v)
			case count < 10:
				in <- fmt.Sprintf("http://localhost:8080/status?key=%s", v)
			case count == 10:
				in <- fmt.Sprintf("http://localhost:8080/unlock?key=%s", v)
			case count < 20:
				in <- fmt.Sprintf("http://localhost:8080/status?key=%s", v)
			default:
				b.Errorf("unreachable")
			}
			actionMap[v] += 1
		}
		close(in)
	}(ch)
	b.ResetTimer()
	wg := sync.WaitGroup{}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(in chan string) {
			for p := range ch {
				_, err := client.Get(p)
				if err != nil {
					b.Error(err)
				}
			}
			wg.Done()
		}(ch)
	}
	wg.Wait()
	k <- struct{}{}
}

func BenchmarkMajorityReads(b *testing.B) {
	k := RunLocalhost()
	client := http.Client{}
	ch := make(chan string, 10)
	randKeys := make([]string, 50)
	for ix := range randKeys {
		randKeys[ix] = strconv.Itoa(rand.Int())
	}
	actions := []string{}
	for i := 0; i < 20; i++ {
		actions = append(actions, randKeys[:]...)
	}
	actionMap := map[string]int{}
	go func(in chan string) {
		for _, v := range actions {
			count := actionMap[v]
			switch {
			case count < 10:
				in <- fmt.Sprintf("http://localhost:8080/rlock?key=%s", v)
			case count < 19:
				in <- fmt.Sprintf("http://localhost:8080/runlock?key=%s", v)
			case count < 20:
				in <- fmt.Sprintf("http://localhost:8080/status?key=%s", v)
			default:
				b.Errorf("unreachable")
			}
			actionMap[v] += 1
		}
		close(in)
	}(ch)
	b.ResetTimer()
	wg := sync.WaitGroup{}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(in chan string) {
			for p := range ch {
				resp, err := client.Get(p)
				if err != nil {
					b.Error(err)
				}
				if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
					b.Errorf("bad status got %v", resp.StatusCode)
				}
			}
			wg.Done()
		}(ch)
	}
	wg.Wait()
	k <- struct{}{}
}
