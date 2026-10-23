package core

import (
	"context"

	types "github.com/JieWaZi/neux-knowledge/internal/domain"
	"github.com/JieWaZi/neux-knowledge/internal/wiki"
)

// GraphRequest 指定图谱模式、类型过滤与邻域参数。
type GraphRequest = types.WikiGraphRequest

// GraphData 包含节点、关系及截断信息。
type GraphData = types.WikiGraphData

// Graph 根据实际页面元数据计算图谱子集。
func (e *Engine) Graph(ctx context.Context, in GraphRequest) (*GraphData, error) {
	if in.KnowledgeBaseID == "" || (in.Mode != "" && in.Mode != "overview" && in.Mode != "ego") || in.Limit < 1 || in.Limit > 1000 || in.Depth < 0 || in.Depth > 3 {
		return nil, &Error{Code: ErrInvalid, Operation: "graph"}
	}
	pages, err := e.store.GraphPages(ctx, in.KnowledgeBaseID)
	if err != nil {
		return nil, err
	}
	return wiki.ComputeGraphSubset(pages, &in)
}
