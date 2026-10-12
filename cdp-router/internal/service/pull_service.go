package service

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"

	taskv1 "github.com/GitMarkZhang/cdp-router/api/task/v1"
	"github.com/GitMarkZhang/cdp-router/internal/biz"
	"github.com/GitMarkZhang/cdp-router/internal/config"
	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/encoding/protojson"
)

type pullProvider struct {
	cfg     config.ProviderConfig
	limiter *rate.Limiter
	slots   chan struct{}
}
type PullService struct {
	pull      *biz.PullUsecase
	tasks     *TaskService
	providers map[string]pullProvider
}

func NewPullService(pull *biz.PullUsecase, tasks *TaskService, configs map[string]config.ProviderConfig) *PullService {
	s := &PullService{pull: pull, tasks: tasks, providers: map[string]pullProvider{}}
	for code, cfg := range configs {
		cfg = config.NormalizeProviderConfig(cfg)
		cfg.Code = code
		if cfg.InteractionMode == "pull" && cfg.Enabled {
			s.providers[code] = pullProvider{cfg: cfg, limiter: rate.NewLimiter(rate.Limit(cfg.QPSLimit), cfg.QPSLimit), slots: make(chan struct{}, cfg.ConcurrencyLimit)}
		}
	}
	return s
}
func (s *PullService) authenticate(c *gin.Context) (config.ProviderConfig, func(), bool) {
	token := c.GetHeader("X-Provider-Token")
	for _, p := range s.providers {
		if token != "" && p.cfg.Token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(p.cfg.Token)) == 1 {
			if !p.limiter.Allow() {
				c.AbortWithStatus(http.StatusTooManyRequests)
				return config.ProviderConfig{}, nil, false
			}
			select {
			case p.slots <- struct{}{}:
			default:
				c.AbortWithStatus(http.StatusTooManyRequests)
				return config.ProviderConfig{}, nil, false
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, p.cfg.PullMaxBodyBytes)
			return p.cfg, func() { <-p.slots }, true
		}
	}
	c.AbortWithStatus(http.StatusUnauthorized)
	return config.ProviderConfig{}, nil, false
}
func (s *PullService) GetTask(c *gin.Context) {
	cfg, release, ok := s.authenticate(c)
	if !ok {
		return
	}
	defer release()
	var req taskv1.PullTaskRequest
	if _, err := bindProto(c, &req); err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	shard, err := s.pull.Claim(c.Request.Context(), biz.ProviderSettings{ProviderCode: cfg.Code, InteractionMode: cfg.InteractionMode, SupportedDevices: cfg.SupportedDevices, DeviceChannels: cfg.DeviceChannels, Enabled: cfg.Enabled}, biz.PullClaimInput{RequestID: req.RequestId, Device: req.Device, Channel: req.Channel, Limit: int(req.Limit)}, cfg.PullMaxItems)
	if err != nil {
		writeBizError(c, err)
		return
	}
	data := &taskv1.PullTaskData{BatchNo: shard.BatchNo, ShardId: shard.ID, Device: shard.Device, Channel: shard.Channel, Items: []*taskv1.TaskItem{}}
	for _, i := range shard.Items {
		data.Items = append(data.Items, &taskv1.TaskItem{ItemId: i.ItemID, PlatformCode: i.PlatformCode, Seq: int32(i.Seq), Question: i.Question, Deepthink: i.Deepthink, WebSearch: i.WebSearch})
	}
	protoJSON(c, http.StatusOK, &taskv1.PullTaskResponse{Data: data})
}
func (s *PullService) SubmitResult(c *gin.Context) {
	cfg, release, ok := s.authenticate(c)
	if !ok {
		return
	}
	defer release()
	var req taskv1.PullResultRequest
	if _, err := bindProto(c, &req); err != nil {
		errorJSON(c, http.StatusBadRequest, err)
		return
	}
	raw, err := protojson.Marshal(&req)
	if err != nil {
		errorJSON(c, 400, err)
		return
	}
	var input biz.PullSubmissionInput
	if err = json.Unmarshal(raw, &input); err != nil {
		errorJSON(c, 400, err)
		return
	}
	id, err := s.pull.Accept(c.Request.Context(), cfg.Code, input, cfg.PullMaxItems)
	if err != nil {
		writeBizError(c, err)
		return
	}
	protoJSON(c, http.StatusAccepted, &taskv1.PullResultResponse{Data: &taskv1.PullResultData{SubmissionId: id, Accepted: true}})
}
func (s *PullService) Progress(c *gin.Context) {
	if s.pull == nil || s.tasks == nil {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	user, ok := s.tasks.requirePlatformUser(c)
	if !ok {
		return
	}
	var task *biz.Task
	var err error
	if job := c.Param("externalBatchId"); job != "" {
		task, err = s.tasks.tasks.GetExternalTask(c.Request.Context(), job, user.ID)
	} else {
		id, parseErr := strconv.ParseInt(c.Param("id"), 10, 64)
		if parseErr != nil {
			errorJSON(c, 400, parseErr)
			return
		}
		task, err = s.tasks.tasks.GetTask(c.Request.Context(), id)
		if err == nil && task.PlatformUserID != user.ID {
			err = biz.ErrTaskNotFound
		}
		if err == nil {
			task, err = s.tasks.tasks.GetExternalTask(c.Request.Context(), task.JobID, user.ID)
		}
	}
	if err != nil {
		writeBizError(c, err)
		return
	}
	progress, err := s.pull.Progress(c.Request.Context(), task.ID)
	if err != nil {
		writeBizError(c, err)
		return
	}
	protoJSON(c, 200, &taskv1.TaskProgressResponse{Data: progressProto(progress)})
}
func progressProto(p biz.PullProgress) *taskv1.TaskProgressData {
	return &taskv1.TaskProgressData{TaskId: p.TaskID, Device: p.Device, ExecutionMode: p.ExecutionMode, Status: string(p.Status), Total: int32(p.Total), Completed: int32(p.Completed), Success: int32(p.Success), Failed: int32(p.Failed), Unclaimed: int32(p.Unclaimed), Shards: int32(p.Shards), CompletedShards: int32(p.CompletedShards), PendingSubmissions: int32(p.PendingSubmissions), FailedSubmissions: int32(p.FailedSubmissions)}
}
