package usage

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Middleware records tools/call responses. sizes returns the total
// indexed size of the named files.
func (r *Recorder) Middleware(sizes func(paths []string) (int64, error)) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if err != nil || method != "tools/call" || r == nil {
				return res, err
			}
			params, ok := req.GetParams().(*mcp.CallToolParamsRaw)
			if !ok || params == nil {
				return res, err
			}
			out, ok := res.(*mcp.CallToolResult)
			if !ok || out == nil {
				return res, err
			}
			n, empty, files := ResultStats(out.StructuredContent)
			var fileBytes int64
			if sizes != nil && len(files) > 0 {
				if total, serr := sizes(files); serr == nil {
					fileBytes = total
				}
			}
			r.Record(params.Name, n, empty, EstimateSaved(params.Name, n, fileBytes))
			return res, err
		}
	}
}
