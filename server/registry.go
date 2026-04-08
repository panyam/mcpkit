package server

import (
	core "github.com/panyam/mcpkit/core"
	"sync"
)

// RegistryChangeFunc is called after a registry mutation with the
// notification method that should be broadcast (e.g.,
// "notifications/tools/list_changed").
type RegistryChangeFunc func(method string)

// Registry holds all tool, resource, prompt, and completion registrations.
// Shared by reference across all session Dispatchers so that dynamic
// adds/removes are immediately visible to every session.
// Protected by mu: readers (list/call handlers) acquire RLock,
// writers (AddTool/RemoveTool etc.) acquire Lock.
//
// When OnChange is set, it is called after every mutation with the
// appropriate list_changed notification method. The Server wires this
// to Broadcast so that connected clients are notified automatically.
type Registry struct {
	mu       sync.RWMutex
	OnChange RegistryChangeFunc // called after mutations; nil = no notification

	tools     map[string]toolEntry
	toolOrder []string

	resources     map[string]resourceEntry
	resourceOrder []string
	templates     map[string]templateEntry
	templateOrder []string

	prompts     map[string]promptEntry
	promptOrder []string

	completions map[string]core.CompletionHandler // key: "ref/prompt:name" or "ref/resource:uri"
}

// NewRegistry creates an empty registry with initialized maps.
func NewRegistry() *Registry {
	return &Registry{
		tools:       make(map[string]toolEntry),
		resources:   make(map[string]resourceEntry),
		templates:   make(map[string]templateEntry),
		prompts:     make(map[string]promptEntry),
		completions: make(map[string]core.CompletionHandler),
	}
}

// notify calls OnChange if set. Must be called outside the write lock.
func (r *Registry) notify(method string) {
	if r.OnChange != nil {
		r.OnChange(method)
	}
}

// AddTool adds a tool to the registry. Thread-safe.
// Broadcasts notifications/tools/list_changed if OnChange is set.
func (r *Registry) AddTool(def core.ToolDef, handler core.ToolHandler) {
	r.mu.Lock()
	r.tools[def.Name] = toolEntry{def: def, handler: handler}
	r.toolOrder = append(r.toolOrder, def.Name)
	r.mu.Unlock()
	r.notify("notifications/tools/list_changed")
}

// RemoveTool removes a tool by name. Returns true if it existed. Thread-safe.
// Broadcasts notifications/tools/list_changed if the tool was removed.
func (r *Registry) RemoveTool(name string) bool {
	r.mu.Lock()
	_, ok := r.tools[name]
	if ok {
		delete(r.tools, name)
		r.toolOrder = removeFromOrder(r.toolOrder, name)
	}
	r.mu.Unlock()
	if ok {
		r.notify("notifications/tools/list_changed")
	}
	return ok
}

// AddResource adds a resource to the registry. Thread-safe.
// Broadcasts notifications/resources/list_changed if OnChange is set.
func (r *Registry) AddResource(def core.ResourceDef, handler core.ResourceHandler) {
	r.mu.Lock()
	r.resources[def.URI] = resourceEntry{def: def, handler: handler}
	r.resourceOrder = append(r.resourceOrder, def.URI)
	r.mu.Unlock()
	r.notify("notifications/resources/list_changed")
}

// RemoveResource removes a resource by URI. Returns true if it existed. Thread-safe.
// Broadcasts notifications/resources/list_changed if the resource was removed.
func (r *Registry) RemoveResource(uri string) bool {
	r.mu.Lock()
	_, ok := r.resources[uri]
	if ok {
		delete(r.resources, uri)
		r.resourceOrder = removeFromOrder(r.resourceOrder, uri)
	}
	r.mu.Unlock()
	if ok {
		r.notify("notifications/resources/list_changed")
	}
	return ok
}

// AddResourceTemplate adds a resource template to the registry. Thread-safe.
// Broadcasts notifications/resources/list_changed if OnChange is set.
func (r *Registry) AddResourceTemplate(def core.ResourceTemplate, handler core.TemplateHandler) {
	r.mu.Lock()
	r.templates[def.URITemplate] = templateEntry{def: def, handler: handler}
	r.templateOrder = append(r.templateOrder, def.URITemplate)
	r.mu.Unlock()
	r.notify("notifications/resources/list_changed")
}

// RemoveResourceTemplate removes a resource template by URI template.
// Returns true if it existed. Thread-safe.
// Broadcasts notifications/resources/list_changed if the template was removed.
func (r *Registry) RemoveResourceTemplate(uriTemplate string) bool {
	r.mu.Lock()
	_, ok := r.templates[uriTemplate]
	if ok {
		delete(r.templates, uriTemplate)
		r.templateOrder = removeFromOrder(r.templateOrder, uriTemplate)
	}
	r.mu.Unlock()
	if ok {
		r.notify("notifications/resources/list_changed")
	}
	return ok
}

// AddPrompt adds a prompt to the registry. Thread-safe.
// Broadcasts notifications/prompts/list_changed if OnChange is set.
func (r *Registry) AddPrompt(def core.PromptDef, handler core.PromptHandler) {
	r.mu.Lock()
	r.prompts[def.Name] = promptEntry{def: def, handler: handler}
	r.promptOrder = append(r.promptOrder, def.Name)
	r.mu.Unlock()
	r.notify("notifications/prompts/list_changed")
}

// RemovePrompt removes a prompt by name. Returns true if it existed. Thread-safe.
// Broadcasts notifications/prompts/list_changed if the prompt was removed.
func (r *Registry) RemovePrompt(name string) bool {
	r.mu.Lock()
	_, ok := r.prompts[name]
	if ok {
		delete(r.prompts, name)
		r.promptOrder = removeFromOrder(r.promptOrder, name)
	}
	r.mu.Unlock()
	if ok {
		r.notify("notifications/prompts/list_changed")
	}
	return ok
}

// AddCompletion adds a completion handler. Thread-safe.
func (r *Registry) AddCompletion(refType, name string, handler core.CompletionHandler) {
	r.mu.Lock()
	r.completions[refType+":"+name] = handler
	r.mu.Unlock()
}

// removeFromOrder removes the first occurrence of key from a string slice.
func removeFromOrder(order []string, key string) []string {
	for i, v := range order {
		if v == key {
			return append(order[:i], order[i+1:]...)
		}
	}
	return order
}
