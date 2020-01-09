// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package nomad

import (
	"regexp"

	"github.com/imdario/mergo"

	"github.com/elastic/beats/libbeat/common"
	"github.com/elastic/beats/libbeat/common/safemapstr"
)

var (
	envRe = regexp.MustCompile(`\${[a-zA-Z0-9_\-\.]+}`)
)

// MetaGenerator builds metadata objects for allocations
type MetaGenerator interface {
	// ResourceMetadata generates metadata for the given allocation
	ResourceMetadata(obj Resource) common.MapStr

	// AllocationNodeName returns the name of the node where the Task is allocated
	AllocationNodeName(id string) (string, error)

	// GroupMeta returns per-task metadata merged with the group metadata, task
	// metadata takes will overwrite metadata from the group with the same key
	GroupMeta(job *Job) []common.MapStr
}

// MetaGeneratorConfig settings
type MetaGeneratorConfig struct {
	IncludeLabels []string `config:"include_labels"`
	ExcludeLabels []string `config:"exclude_labels"`

	// Undocumented settings, to be deprecated in favor of `drop_fields` processor:
	LabelsDedot bool `config:"labels.dedot"`
	client      *Client
	nodesCache  map[string]string
}

type metaGenerator = MetaGeneratorConfig

// NewMetaGenerator initializes and returns a new nomad metadata generator
func NewMetaGenerator(cfg *common.Config, c *Client) (MetaGenerator, error) {
	// default settings:
	generator := metaGenerator{
		LabelsDedot: true,
		client:      c,
	}

	err := cfg.Unpack(&generator)
	return &generator, err
}

// NewMetaGeneratorFromConfig initializes and returns a new nomad metadata generator
func NewMetaGeneratorFromConfig(cfg *MetaGeneratorConfig) MetaGenerator {
	return cfg
}

// ResourceMetadata generates metadata for the given Nomad allocation*
func (g *metaGenerator) ResourceMetadata(obj Resource) common.MapStr {
	// default labels that we expose / filter with `IncludeLabels`
	meta := common.MapStr{
		"name":        obj.Name,
		"job":         *obj.Job.Name,
		"namespace":   obj.Namespace,
		"datacenters": obj.Job.Datacenters,
		"region":      *obj.Job.Region,
		"type":        *obj.Job.Type,
		"alloc_id":    obj.ID,
		"status":      *obj.Job.Status,
	}

	return meta
}

// Returns an array of per-task metadata aggregating the group metadata into the
// task metadata
func (g *metaGenerator) GroupMeta(job *Job) []common.MapStr {
	tasksMeta := []common.MapStr{}

	for _, group := range job.TaskGroups {
		// TODO: copy of job.Meta
		meta := make(map[string]string)
		for k, v := range job.Meta {
			meta[k] = v
		}

		mergo.Merge(&meta, group.Meta, mergo.WithOverride)
		group.Meta = meta

		tasks := g.tasksMeta(group)
		tasksMeta = append(tasksMeta, tasks...)
	}

	for idx, task := range tasksMeta {
		labelMap := common.MapStr{}

		if len(g.IncludeLabels) == 0 {
			for k, v := range task {
				if g.LabelsDedot {
					label := common.DeDot(k)
					labelMap.Put(label, v)
				} else {
					safemapstr.Put(labelMap, k, v)
				}
			}
		} else {
			labelMap = generateMapSubset(task, g.IncludeLabels, g.LabelsDedot)
		}

		// Exclude any labels that are present in the exclude_labels config
		for _, label := range g.ExcludeLabels {
			labelMap.Delete(label)
		}

		tasksMeta[idx] = labelMap
	}

	return tasksMeta
}

// Returns per-task metadata
func (g *metaGenerator) tasksMeta(group *TaskGroup) []common.MapStr {
	tasks := []common.MapStr{}

	for _, task := range group.Tasks {
		svcMeta := common.MapStr{
			"name":        []string{},
			"tags":        []string{},
			"canary_tags": []string{},
		}

		for _, service := range task.Services {
			svcMeta["name"] = append(svcMeta["name"].([]string), service.Name)
			svcMeta["tags"] = append(svcMeta["tags"].([]string), service.Tags...)
			svcMeta["canary_tags"] = append(svcMeta["canary_tags"].([]string),
				service.CanaryTags...)
		}

		joinMeta := group.Meta
		mergo.Merge(&joinMeta, task.Meta, mergo.WithOverride)

		meta := common.MapStr{
			"name":    task.Name,
			"service": svcMeta,
		}

		for k, v := range joinMeta {
			meta.Put(k, v)
		}

		tasks = append(tasks, meta)
	}

	return tasks
}

func generateMapSubset(input common.MapStr, keys []string, dedot bool) common.MapStr {
	output := common.MapStr{}
	if input == nil {
		return output
	}

	for _, key := range keys {
		value, ok := input[key]
		if ok {
			if dedot {
				dedotKey := common.DeDot(key)
				output.Put(dedotKey, value)
			} else {
				safemapstr.Put(output, key, value)
			}
		}
	}

	return output
}

// AllocationNodeName returns Name of the node where the task is allocated. It
// does one additional API call to circunvent the empty NodeName property of
// older Nomad versions (up to v0.8)
func (g *metaGenerator) AllocationNodeName(id string) (string, error) {
	if name, ok := g.nodesCache[id]; ok {
		return name, nil
	}

	node, _, err := g.client.Nodes().Info(id, nil)
	if err != nil {
		return "", err
	}

	g.nodesCache[id] = node.Name

	return node.Name, nil
}
