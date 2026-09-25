package k8splugin

import "context"

// FakeBackend stands in for a cluster at the Backend boundary. It is the seam
// used to test dispatch, defaulting and scoping — the plumbing above the
// mapping. The mapping itself is tested against client-go's own fake clientset
// in clientgo_test.go, which drives the real code in clientgo.go.
//
// It records what it was called with, so a test can assert that per-call
// context selection actually reached the backend and that the next call did not
// inherit the last one's choice.
type FakeBackend struct {
	HealthResult    Health
	NamespaceResult List[Namespace]
	NodeResult      List[Node]
	PodResult       List[Pod]
	WorkloadResult  List[Workload]
	EventResult     List[Event]
	LogResult       LogSnapshot
	DetailResult    WorkloadDetail
	ServiceResult   List[Service]
	IngressResult   List[Ingress]
	APIResult       APIResourceList
	UsageResult     Usage
	ChangeResult    Change
	Err             error

	LastOptions   ClusterOptions
	LastListQuery ListQuery
	LastPodQuery  PodQuery
	LastWorkload  WorkloadQuery
	LastEvent     EventQuery
	LastLog       LogQuery
	LastRef       WorkloadRef
	LastScoped    ScopedQuery
	LastAPI       APIResourceQuery
	LastTop       TopQuery
	LastScale     ScaleRequest
	LastRestart   RestartRequest
	LastNode      SchedulableRequest
	LastDelete    DeletePodRequest
	Calls         []string
}

var _ Backend = (*FakeBackend)(nil)

func (f *FakeBackend) record(call string, opts ClusterOptions) {
	f.Calls = append(f.Calls, call)
	f.LastOptions = opts
}

func (f *FakeBackend) Health(_ context.Context, opts ClusterOptions) (Health, error) {
	f.record("Health", opts)
	return f.HealthResult, f.Err
}

func (f *FakeBackend) Namespaces(_ context.Context, opts ClusterOptions, query ListQuery) (List[Namespace], error) {
	f.record("Namespaces", opts)
	f.LastListQuery = query
	return f.NamespaceResult, f.Err
}

func (f *FakeBackend) Nodes(_ context.Context, opts ClusterOptions, query ListQuery) (List[Node], error) {
	f.record("Nodes", opts)
	f.LastListQuery = query
	return f.NodeResult, f.Err
}

func (f *FakeBackend) Pods(_ context.Context, opts ClusterOptions, query PodQuery) (List[Pod], error) {
	f.record("Pods", opts)
	f.LastPodQuery = query
	return f.PodResult, f.Err
}

func (f *FakeBackend) Workloads(_ context.Context, opts ClusterOptions, query WorkloadQuery) (List[Workload], error) {
	f.record("Workloads", opts)
	f.LastWorkload = query
	return f.WorkloadResult, f.Err
}

func (f *FakeBackend) Events(_ context.Context, opts ClusterOptions, query EventQuery) (List[Event], error) {
	f.record("Events", opts)
	f.LastEvent = query
	return f.EventResult, f.Err
}

func (f *FakeBackend) Logs(_ context.Context, opts ClusterOptions, query LogQuery) (LogSnapshot, error) {
	f.record("Logs", opts)
	f.LastLog = query
	return f.LogResult, f.Err
}

func (f *FakeBackend) DescribeWorkload(_ context.Context, opts ClusterOptions, ref WorkloadRef) (WorkloadDetail, error) {
	f.record("DescribeWorkload", opts)
	f.LastRef = ref
	return f.DetailResult, f.Err
}

func (f *FakeBackend) Services(_ context.Context, opts ClusterOptions, query ScopedQuery) (List[Service], error) {
	f.record("Services", opts)
	f.LastScoped = query
	return f.ServiceResult, f.Err
}

func (f *FakeBackend) Ingresses(_ context.Context, opts ClusterOptions, query ScopedQuery) (List[Ingress], error) {
	f.record("Ingresses", opts)
	f.LastScoped = query
	return f.IngressResult, f.Err
}

func (f *FakeBackend) APIResources(_ context.Context, opts ClusterOptions, query APIResourceQuery) (APIResourceList, error) {
	f.record("APIResources", opts)
	f.LastAPI = query
	return f.APIResult, f.Err
}

func (f *FakeBackend) Top(_ context.Context, opts ClusterOptions, query TopQuery) (Usage, error) {
	f.record("Top", opts)
	f.LastTop = query
	return f.UsageResult, f.Err
}

func (f *FakeBackend) Scale(_ context.Context, opts ClusterOptions, req ScaleRequest) (Change, error) {
	f.record("Scale", opts)
	f.LastScale = req
	return f.ChangeResult, f.Err
}

func (f *FakeBackend) Restart(_ context.Context, opts ClusterOptions, req RestartRequest) (Change, error) {
	f.record("Restart", opts)
	f.LastRestart = req
	return f.ChangeResult, f.Err
}

func (f *FakeBackend) SetSchedulable(_ context.Context, opts ClusterOptions, req SchedulableRequest) (Change, error) {
	f.record("SetSchedulable", opts)
	f.LastNode = req
	return f.ChangeResult, f.Err
}

func (f *FakeBackend) DeletePod(_ context.Context, opts ClusterOptions, req DeletePodRequest) (Change, error) {
	f.record("DeletePod", opts)
	f.LastDelete = req
	return f.ChangeResult, f.Err
}
