package service

import (
	"sort"
	"time"

	"github.com/alireza0/s-ui/database"
	"github.com/alireza0/s-ui/database/model"

	"gorm.io/gorm"
)

type onlines struct {
	Inbound  []string `json:"inbound,omitempty"`
	User     []string `json:"user,omitempty"`
	Outbound []string `json:"outbound,omitempty"`
}

var onlineResources = &onlines{}

type StatsService struct {
}

const (
	defaultTrafficPoolBytes     int64 = 1000 * 1024 * 1024 * 1024
	defaultTrafficPoolCycleDays       = 30
	defaultTrafficPoolSource          = "user"
)

var trafficPoolNow = time.Now

type TrafficPoolUser struct {
	Name  string `json:"name"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
	Total int64  `json:"total"`
}

type TrafficPoolSummary struct {
	Limit       int64             `json:"limit"`
	Used        int64             `json:"used"`
	Remaining   int64             `json:"remaining"`
	Percent     int64             `json:"percent"`
	StartedAt   int64             `json:"startedAt"`
	EndedAt     int64             `json:"endedAt"`
	NextResetAt int64             `json:"nextResetAt"`
	CycleDays   int               `json:"cycleDays"`
	Source      string            `json:"source"`
	Users       []TrafficPoolUser `json:"users"`
}

func (s *StatsService) SaveStats(enableTraffic bool) error {
	if corePtr == nil || !corePtr.IsRunning() {
		return nil
	}
	box := corePtr.GetInstance()
	if box == nil {
		return nil
	}
	st := box.StatsTracker()
	if st == nil {
		return nil
	}
	stats := st.GetStats()

	// Reset onlines
	onlineResources.Inbound = nil
	onlineResources.Outbound = nil
	onlineResources.User = nil

	if len(*stats) == 0 {
		return nil
	}

	var err error
	db := database.GetDB()
	tx := db.Begin()
	defer func() {
		if err == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()

	for _, stat := range *stats {
		if stat.Resource == "user" {
			if stat.Direction {
				err = tx.Model(model.Client{}).Where("name = ?", stat.Tag).
					UpdateColumn("up", gorm.Expr("up + ?", stat.Traffic)).Error
			} else {
				err = tx.Model(model.Client{}).Where("name = ?", stat.Tag).
					UpdateColumn("down", gorm.Expr("down + ?", stat.Traffic)).Error
			}
			if err != nil {
				return err
			}
		}
		if stat.Direction {
			switch stat.Resource {
			case "inbound":
				onlineResources.Inbound = append(onlineResources.Inbound, stat.Tag)
			case "outbound":
				onlineResources.Outbound = append(onlineResources.Outbound, stat.Tag)
			case "user":
				onlineResources.User = append(onlineResources.User, stat.Tag)
			}
		}
	}

	if !enableTraffic {
		return nil
	}
	err = tx.Create(&stats).Error
	return err
}

func (s *StatsService) GetStats(resource string, tag string, limit int) ([]model.Stats, error) {
	var err error
	var result []model.Stats

	currentTime := time.Now().Unix()
	timeDiff := currentTime - (int64(limit) * 3600)

	db := database.GetDB()
	resources := []string{resource}
	if resource == "endpoint" {
		resources = []string{"inbound", "outbound"}
	}
	err = db.Model(model.Stats{}).Where("resource in ? AND tag = ? AND date_time > ?", resources, tag, timeDiff).Scan(&result).Error
	if err != nil {
		return nil, err
	}

	result = s.downsampleStats(result, 60) // 60 rows for 30 buckets
	return result, nil
}

// downsampleStats reduces stats to maxRows rows.
// Each bucket outputs two rows (direction false and true) with average Traffic.
func (s *StatsService) downsampleStats(stats []model.Stats, maxRows int) []model.Stats {
	if len(stats) <= maxRows {
		return stats
	}
	numBuckets := int(maxRows / 2)
	sort.Slice(stats, func(i, j int) bool { return stats[i].DateTime < stats[j].DateTime })
	timeMin, timeMax := stats[0].DateTime, stats[len(stats)-1].DateTime
	bucketSpan := (timeMax - timeMin) / int64(numBuckets)
	if bucketSpan == 0 {
		bucketSpan = 1
	}
	downsampled := make([]model.Stats, 0, maxRows)
	for i := 0; i < numBuckets; i++ {
		bucketStart := timeMin + int64(i)*bucketSpan
		bucketEnd := timeMin + int64(i+1)*bucketSpan
		if i == numBuckets-1 {
			bucketEnd = timeMax + 1
		}
		for _, dir := range []bool{false, true} {
			var sum int64
			var count int
			for _, r := range stats {
				if r.DateTime >= bucketStart && r.DateTime < bucketEnd && r.Direction == dir {
					sum += r.Traffic
					count++
				}
			}
			avg := int64(0)
			if count > 0 {
				avg = sum / int64(count)
			}
			downsampled = append(downsampled, model.Stats{
				DateTime:  bucketStart,
				Resource:  stats[0].Resource,
				Tag:       stats[0].Tag,
				Direction: dir,
				Traffic:   avg,
			})
		}
	}
	return downsampled
}

func (s *StatsService) GetOnlines() (onlines, error) {
	return *onlineResources, nil
}

func (s *StatsService) GetTrafficPool() (*TrafficPoolSummary, error) {
	config, err := (&SettingService{}).GetTrafficPoolConfig()
	if err != nil {
		return nil, err
	}
	start, end := trafficPoolWindow(config.AnchorAt, config.CycleDays, trafficPoolNow())

	db := database.GetDB()
	var users []TrafficPoolUser
	err = db.Model(model.Stats{}).
		Select(`tag as name,
			sum(case when direction = true then traffic else 0 end) as up,
			sum(case when direction = false then traffic else 0 end) as down,
			sum(traffic) as total`).
		Where("resource = ? AND date_time >= ? AND date_time < ?", config.Source, start, end).
		Group("tag").
		Order("total desc").
		Scan(&users).Error
	if err != nil {
		return nil, err
	}

	var used int64
	for _, user := range users {
		used += user.Total
	}
	remaining := config.LimitBytes - used
	if remaining < 0 {
		remaining = 0
	}
	percent := int64(0)
	if config.LimitBytes > 0 {
		percent = used * 100 / config.LimitBytes
	}

	if len(users) > 5 {
		users = users[:5]
	}

	return &TrafficPoolSummary{
		Limit:       config.LimitBytes,
		Used:        used,
		Remaining:   remaining,
		Percent:     percent,
		StartedAt:   start,
		EndedAt:     end,
		NextResetAt: end,
		CycleDays:   config.CycleDays,
		Source:      config.Source,
		Users:       users,
	}, nil
}

func trafficPoolWindow(anchorAt int64, cycleDays int, now time.Time) (int64, int64) {
	cycleSeconds := int64(cycleDays) * 86400
	if cycleSeconds <= 0 {
		cycleSeconds = int64(defaultTrafficPoolCycleDays) * 86400
	}
	nowUnix := now.UTC().Unix()
	if anchorAt <= 0 {
		anchorAt = nowUnix
	}
	if nowUnix < anchorAt {
		return anchorAt, anchorAt + cycleSeconds
	}
	start := anchorAt + ((nowUnix-anchorAt)/cycleSeconds)*cycleSeconds
	return start, start + cycleSeconds
}

func (s *StatsService) DelOldStats(days int) error {
	oldTime := time.Now().AddDate(0, 0, -(days)).Unix()
	db := database.GetDB()
	return db.Where("date_time < ?", oldTime).Delete(model.Stats{}).Error
}
