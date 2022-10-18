package pipeline

import (
	"embed"
	"encoding/json"
	"fmt"
	"github.com/ozontech/file.d/logger"
	"go.uber.org/atomic"
	"net/http"
	"text/template"
)

//go:embed htmltpl
var pipelineTpl embed.FS

type pluginsObservabilityInfo struct {
	In            inObservabilityInfo       `json:"in"`
	Out           outObservabilityInfo      `json:"out"`
	ActionPlugins []actionObservabilityInfo `json:"actions"`
	LogChanges    logChangesDTO             `json:"log_changes"`
}

type inObservabilityInfo struct {
	PluginName string `json:"plugin_name"`
}

type outObservabilityInfo struct {
	PluginName      string           `json:"plugin_name"`
	BatcherCounters []batcherCounter `json:"batcher_counters"`
	BatcherMinWait  BatcherTimeDTO   `json:"batcher_min_wait"`
	BatcherMaxWait  BatcherTimeDTO   `json:"batcher_max_wait"`
}

type batcherCounter struct {
	Seconds          int64 `json:"seconds"`
	BatchesCommitted int64 `json:"batches_committed"`
}

type actionObservabilityInfo struct {
	PluginName string              `json:"plugin_name"`
	MetricName string              `json:"metric_name"`
	Tracked    bool                `json:"tracked"`
	Statuses   []actionEventStatus `json:"statuses"`
}

type actionEventStatus struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
	Color string `json:"color"`
}

func (p *Pipeline) boardInfo(
	inputInfo *InputPluginInfo,
	actionInfos []*ActionPluginStaticInfo,
	outputInfo *OutputPluginInfo,
) pluginsObservabilityInfo {
	result := pluginsObservabilityInfo{}

	inputEvents := newDeltaWrapper()
	inputSize := newDeltaWrapper()
	outputEvents := newDeltaWrapper()
	outputSize := newDeltaWrapper()
	readOps := newDeltaWrapper()

	localDeltas := p.incMetrics(inputEvents, inputSize, outputEvents, outputSize, readOps)
	result.LogChanges = p.logChanges(localDeltas)

	in := inObservabilityInfo{
		PluginName: inputInfo.Type,
	}
	result.In = in

	batcherCounters := make([]batcherCounter, 0, len(batcherTimeKeys))
	obsInfo := p.output.GetObservabilityInfo()
	if obsInfo.BatcherInformation.CommittedCounters != nil {
		for _, timePoint := range batcherTimeKeys {
			cnt, ok := obsInfo.BatcherInformation.CommittedCounters[timePoint]
			if !ok {
				cnt = 0
			}
			pair := batcherCounter{
				Seconds:          timePoint,
				BatchesCommitted: cnt,
			}
			batcherCounters = append(batcherCounters, pair)
		}
	}

	out := outObservabilityInfo{
		PluginName: outputInfo.Type,
	}
	out.BatcherCounters = batcherCounters
	out.BatcherMinWait = obsInfo.BatcherInformation.MinWait
	out.BatcherMaxWait = obsInfo.BatcherInformation.MaxWait

	result.Out = out

	for _, info := range actionInfos {
		action := actionObservabilityInfo{}
		action.PluginName = info.Type
		if info.MetricName == "" {
			result.ActionPlugins = append(result.ActionPlugins, action)
			continue
		}

		action.Tracked = true
		action.MetricName = info.MetricName

		var actionMetric *metrics
		for _, m := range p.metricsHolder.metrics {
			if m.name == info.MetricName {
				actionMetric = m

				for _, status := range []eventStatus{
					eventStatusReceived,
					eventStatusDiscarded,
					eventStatusPassed,
					eventStatusNotMatched,
					eventStatusCollapse,
					eventStatusHold,
				} {
					c := actionMetric.current.totalCounter[string(status)]
					if c == nil {
						c = atomic.NewUint64(0)
					}
					color := "#0d8bf0"
					switch status {
					case eventStatusNotMatched:
						color = "#8bc34a"
					case eventStatusPassed:
						color = "green"
					case eventStatusDiscarded:
						color = "red"
					case eventStatusCollapse:
						color = "#009688"
					case eventStatusHold:
						color = "#f050f0"
					}
					eventStatus := actionEventStatus{Name: string(status), Count: int64(c.Load()), Color: color}
					action.Statuses = append(action.Statuses, eventStatus)
				}
			}
		}
		result.ActionPlugins = append(result.ActionPlugins, action)
	}

	return result
}

func (p *Pipeline) serveBoardInfoJSON(inputInfo *InputPluginInfo,
	actionInfos []*ActionPluginStaticInfo,
	outputInfo *OutputPluginInfo) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Content-Type", "application/json")
		result := p.boardInfo(inputInfo, actionInfos, outputInfo)
		bytes, err := json.Marshal(result)
		if err != nil {
			_, _ = w.Write([]byte(fmt.Sprintf("can't get json info: %s", err.Error())))
		}

		_, _ = w.Write(bytes)
	}
}

func (p *Pipeline) serveBoardInfo(
	inputInfo *InputPluginInfo,
	actionInfos []*ActionPluginStaticInfo,
	outputInfo *OutputPluginInfo) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		tmpl, err := template.ParseFS(pipelineTpl, "htmltpl/pipeline_info.html")
		if err != nil {
			logger.Errorf("can't parse html template: %s", err.Error())
			_, _ = w.Write([]byte(fmt.Sprintf("<hmtl><body>can't parse html: %s", err.Error())))
			return
		}

		funcMap := template.FuncMap{
			// The name "title" is what the function will be called in the template text.
			"title": func(a int) int {
				return a + 1
			},
		}
		/*	tmpl = tmpl.Funcs(template.FuncMap{
			"subtract": func(a, b int) int {
				return a - b
			},
		})*/

		boardInfo := p.boardInfo(inputInfo, actionInfos, outputInfo)
		err = tmpl.Funcs(funcMap).Execute(w, boardInfo)
		if err != nil {
			logger.Errorf("can't execute html template: %s", err.Error())
			_, _ = w.Write([]byte(fmt.Sprintf("<hmtl><body>can't render html: %s", err.Error())))
			return
		}
	}
}
