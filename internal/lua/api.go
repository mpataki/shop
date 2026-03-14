package lua

import (
	lua "github.com/yuin/gopher-lua"

	"github.com/mpataki/shop/internal/models"
)

// luaStuck implements the stuck(reason?) API
func (r *Runtime) luaStuck(L *lua.LState) int {
	reason := L.OptString(1, "workflow stuck")
	r.stuckReason = reason
	r.isStuck = true
	L.RaiseError("stuck: %s", reason)
	return 0
}

// luaContext implements the context() API
func (r *Runtime) luaContext(L *lua.LState) int {
	tbl := L.NewTable()
	L.SetField(tbl, "run_id", lua.LNumber(r.run.ID))
	L.SetField(tbl, "repo", lua.LString(r.ws.RepoPath))
	L.SetField(tbl, "iteration", lua.LNumber(r.callIndex))
	L.SetField(tbl, "prompt", lua.LString(r.run.InitialPrompt))
	L.Push(tbl)
	return 1
}

// luaLog implements the log(message) API
func (r *Runtime) luaLog(L *lua.LState) int {
	message := L.CheckString(1)
	r.logs = append(r.logs, message)
	r.appendEvent(models.WFEventLogMessage, nil, "", models.LogMessagePayload{Message: message})
	return 0
}
