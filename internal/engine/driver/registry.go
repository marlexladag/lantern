package driver

import "sort"
import "sync"

var (
	mu      sync.RWMutex
	drivers = make(map[string]Driver)
)

// Register makes a driver available by id. Drivers call this from an init
// function, so a duplicate id is a programming error, not a runtime
// condition — it panics rather than letting the last init silently win,
// mirroring database/sql.Register.
func Register(d Driver) {
	mu.Lock()
	defer mu.Unlock()
	id := d.ID()
	if _, dup := drivers[id]; dup {
		panic("driver: Register called twice for driver " + id)
	}
	drivers[id] = d
}

// Lookup finds a registered driver.
func Lookup(id string) (Driver, bool) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := drivers[id]
	return d, ok
}

// IDs lists every registered driver id, sorted, so the UI's driver picker has
// a stable order.
func IDs() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(drivers))
	for id := range drivers {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// reset clears the registry. Tests use it so registration order cannot leak
// between cases.
func reset() {
	mu.Lock()
	defer mu.Unlock()
	drivers = make(map[string]Driver)
}
