package driver

import "sort"
import "sync"

var (
	mu      sync.RWMutex
	drivers = make(map[string]Driver)
)

// Register makes a driver available by id, replacing any previous
// registration. Drivers call this from an init function.
func Register(d Driver) {
	mu.Lock()
	defer mu.Unlock()
	drivers[d.ID()] = d
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
