package timezone

import (
	"sync"
	"time"
)

const AdminLocationName = "Asia/Shanghai"

var (
	adminLocationOnce sync.Once
	adminLocation     *time.Location
)

func AdminLocation() *time.Location {
	adminLocationOnce.Do(func() {
		loc, err := time.LoadLocation(AdminLocationName)
		if err != nil {
			loc = time.FixedZone("CST", 8*60*60)
		}
		adminLocation = loc
	})
	return adminLocation
}

func InAdminLocation(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.In(AdminLocation())
}
