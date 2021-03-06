// +build functional

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/argoproj/argo/v2/pkg/apis/workflow"
	wfv1 "github.com/argoproj/argo/v2/pkg/apis/workflow/v1alpha1"
	"github.com/argoproj/argo/v2/test/e2e/fixtures"
)

type FunctionalSuite struct {
	fixtures.E2ESuite
}

func (s *FunctionalSuite) TestArchiveStrategies() {
	s.Need(fixtures.BaseLayerArtifacts)
	s.Given().
		Workflow(`@testdata/archive-strategies.yaml`).
		When().
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
		})
}

// when you delete a pending pod,
// then the pod is re- created automatically
func (s *FunctionalSuite) TestDeletingPendingPod() {
	s.Given().
		Workflow("@testdata/pending-workflow.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow(fixtures.ToStart, "to start").
		Exec("kubectl", []string{"-n", "argo", "delete", "pod", "-l", "workflows.argoproj.io/workflow"}, fixtures.OutputRegexp(`pod "pending-.*" deleted`)).
		Wait(3*time.Second). // allow 3s for reconcilliation, we'll create a new pod
		Exec("kubectl", []string{"-n", "argo", "get", "pod", "-l", "workflows.argoproj.io/workflow"}, fixtures.OutputRegexp(`pending-.*Pending`))
}

// where you delete a running pod,
// then the workflow is failed
func (s *FunctionalSuite) TestDeletingRunningPod() {
	s.Given().
		Workflow("@testdata/sleepy-workflow.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow(fixtures.ToBeRunning, "to be running").
		Wait(10*time.Second). // be very sure the pod is actually running (not pending)
		Exec("kubectl", []string{"-n", "argo", "delete", "pod", "-l", "workflows.argoproj.io/workflow"}, fixtures.OutputRegexp(`pod "sleepy-.*" deleted`)).
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, metadata *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowFailed, status.Phase)
			assert.Contains(t, status.Nodes[metadata.Name].Message, "failed with exit code")
		})
}

// where you delete a running pod, and you have retry on error,
// then the node is retried
func (s *FunctionalSuite) TestDeletingRunningPodWithOrErrorRetryPolicy() {
	s.Given().
		Workflow("@testdata/sleepy-retry-on-error-workflow.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow(fixtures.ToBeRunning, "to be running").
		Exec("kubectl", []string{"-n", "argo", "delete", "pod", "-l", "workflows.argoproj.io/workflow"}, fixtures.NoError).
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, metadata *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
			assert.Len(t, status.Nodes, 2)
		})
}

func (s *FunctionalSuite) TestSynchronizationWfLevelMutex() {
	s.Given().
		Workflow("@functional/synchronization-mutex-wf-level-1.yaml").
		When().
		SubmitWorkflow().
		Given().
		Workflow("@functional/synchronization-mutex-wf-level.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow(fixtures.ToBeWaitingOnAMutex, "to be waiting on a mutex").
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
		})
}

func (s *FunctionalSuite) TestTemplateLevelMutex() {
	s.Given().
		Workflow("@functional/synchronization-mutex-tmpl-level.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow(fixtures.ToBeWaitingOnAMutex, "to be waiting on a mutex").
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
		})
}

func (s *FunctionalSuite) TestWorkflowTTL() {
	s.Given().
		Workflow(`
metadata:
  generateName: workflow-ttl-
  labels:
    argo-e2e: true
spec:
  ttlStrategy:
    secondsAfterCompletion: 0
  entrypoint: main
  templates:
    - name: main
      container:
        image: argoproj/argosay:v2
`).
		When().
		SubmitWorkflow().
		WaitForWorkflow().
		Wait(3 * time.Second). // enough time for TTL controller to delete the workflow
		Then().
		ExpectWorkflowDeleted()
}

// in this test we create a poi quota, and then  we create a workflow that needs one more pod than the quota allows
// because we run them in parallel, the first node will run to completion, and then the second one
func (s *FunctionalSuite) TestResourceQuota() {
	s.Given().
		Workflow(`@testdata/two-items.yaml`).
		When().
		PodsQuota(2).
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
		})
}

func (s *FunctionalSuite) TestContinueOnFail() {
	s.Given().
		Workflow(`
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  name: continue-on-fail
  labels:
    argo-e2e: true
spec:
  entrypoint: workflow-ignore
  parallelism: 2
  templates:
  - name: workflow-ignore
    steps:
    - - name: A
        template: whalesay
      - name: B
        template: boom
        continueOn:
          failed: true
    - - name: C
        template: whalesay

  - name: boom
    dag:
      tasks:
      - name: B-1
        template: whalesplosion

  - name: whalesay
    container:
      image: argoproj/argosay:v2
      imagePullPolicy: IfNotPresent

  - name: whalesplosion
    container:
      image: argoproj/argosay:v2
      imagePullPolicy: IfNotPresent
      command: ["sh", "-c", "sleep 5 ; exit 1"]
`).
		When().
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
			assert.Len(t, status.Nodes, 7)
			nodeStatus := status.Nodes.FindByDisplayName("B")
			if assert.NotNil(t, nodeStatus) {
				assert.Equal(t, wfv1.NodeFailed, nodeStatus.Phase)
				assert.Len(t, nodeStatus.Children, 1)
				assert.Len(t, nodeStatus.OutboundNodes, 1)
			}
		})
}

func (s *FunctionalSuite) TestContinueOnFailDag() {
	s.Given().
		Workflow(`
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  name: continue-on-failed-dag
  labels:
    argo-e2e: true
spec:
  entrypoint: workflow-ignore
  parallelism: 2
  templates:
    - name: workflow-ignore
      dag:
        failFast: false
        tasks:
          - name: A
            template: whalesay
          - name: B
            template: boom
            continueOn:
              failed: true
            dependencies:
              - A
          - name: C
            template: whalesay
            dependencies:
              - A
          - name: D
            template: whalesay
            dependencies:
              - B
              - C

    - name: boom
      dag:
        tasks:
          - name: B-1
            template: whalesplosion

    - name: whalesay
      container:
        imagePullPolicy: IfNotPresent
        image: argoproj/argosay:v2

    - name: whalesplosion
      container:
        imagePullPolicy: IfNotPresent
        image: argoproj/argosay:v2
        command: ["sh", "-c", "sleep 10; exit 1"]
`).
		When().
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
			assert.Len(t, status.Nodes, 6)

			bStatus := status.Nodes.FindByDisplayName("B")
			if assert.NotNil(t, bStatus) {
				assert.Equal(t, wfv1.NodeFailed, bStatus.Phase)
			}

			dStatus := status.Nodes.FindByDisplayName("D")
			if assert.NotNil(t, dStatus) {
				assert.Equal(t, wfv1.NodeSucceeded, dStatus.Phase)
			}
		})
}

func (s *FunctionalSuite) TestEventOnNodeFail() {
	// Test whether an WorkflowFailed event (with appropriate message) is emitted in case of node failure
	var uid types.UID
	s.Given().
		Workflow("@expectedfailures/failed-step-event.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, metadata *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			uid = metadata.UID
		}).
		ExpectAuditEvents(
			fixtures.HasInvolvedObject(workflow.WorkflowKind, uid),
			2,
			func(t *testing.T, es []corev1.Event) {
				for _, e := range es {
					switch e.Reason {
					case "WorkflowRunning":
					case "WorkflowNodeFailed":
						assert.Contains(t, e.Message, "Failed node failed-step-event-")
						assert.Equal(t, e.Annotations["workflows.argoproj.io/node-type"], "Pod")
						assert.Contains(t, e.Annotations["workflows.argoproj.io/node-name"], "failed-step-event-")
					case "WorkflowFailed":
						assert.Contains(t, e.Message, "exit code 1")
					default:
						assert.Fail(t, e.Reason)
					}
				}
			},
		)
}

func (s *FunctionalSuite) TestEventOnWorkflowSuccess() {
	// Test whether an WorkflowSuccess event is emitted in case of successfully completed workflow
	var uid types.UID
	s.Given().
		Workflow("@functional/success-event.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow(60*time.Second).
		Then().
		ExpectWorkflow(func(t *testing.T, metadata *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			uid = metadata.UID
		}).
		ExpectAuditEvents(
			fixtures.HasInvolvedObject(workflow.WorkflowKind, uid),
			3,
			func(t *testing.T, es []corev1.Event) {
				for _, e := range es {
					switch e.Reason {
					case "WorkflowRunning":
					case "WorkflowNodeSucceeded":
						assert.Contains(t, e.Message, "Succeeded node success-event-")
						assert.Equal(t, "Pod", e.Annotations["workflows.argoproj.io/node-type"])
						assert.Contains(t, e.Annotations["workflows.argoproj.io/node-name"], "success-event-")
					case "WorkflowSucceeded":
						assert.Equal(t, "Workflow completed", e.Message)
					default:
						assert.Fail(t, e.Reason)
					}
				}
			},
		)
}

func (s *FunctionalSuite) TestEventOnPVCFail() {
	//  Test whether an WorkflowFailed event (with appropriate message) is emitted in case of error in creating the PVC
	var uid types.UID
	s.Given().
		Workflow("@expectedfailures/volumes-pvc-fail-event.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow(120*time.Second).
		Then().
		ExpectWorkflow(func(t *testing.T, metadata *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			uid = metadata.UID
		}).
		ExpectAuditEvents(
			fixtures.HasInvolvedObject(workflow.WorkflowKind, uid),
			2,
			func(t *testing.T, e []corev1.Event) {
				assert.Equal(t, "WorkflowRunning", e[0].Reason)

				assert.Equal(t, "WorkflowFailed", e[1].Reason)
				assert.Contains(t, e[1].Message, "pvc create error")
			},
		)
}

func (s *FunctionalSuite) TestArtifactRepositoryRef() {
	s.Need(fixtures.BaseLayerArtifacts)
	s.Given().
		Workflow("@testdata/artifact-repository-ref.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, metadata *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
			if assert.NotEmpty(t, status.ArtifactRepositoryRef) {
				assert.Equal(t, "argo", status.ArtifactRepositoryRef.Namespace)
				assert.Equal(t, "artifact-repositories", status.ArtifactRepositoryRef.ConfigMap)
				assert.Equal(t, "my-key", status.ArtifactRepositoryRef.Key)
				assert.False(t, status.ArtifactRepositoryRef.Default)
			}
			// these should never be set because we must get them from the artifactRepositoryRef
			generated := status.Nodes.FindByDisplayName("generate").Outputs.Artifacts[0].S3
			assert.Empty(t, generated.Bucket)
			assert.NotEmpty(t, generated.Key)
			consumed := status.Nodes.FindByDisplayName("consume").Inputs.Artifacts[0].S3
			assert.Empty(t, consumed.Bucket)
			assert.NotEmpty(t, consumed.Key)
		})
}

func (s *FunctionalSuite) TestLoopEmptyParam() {
	s.Given().
		Workflow("@functional/loops-empty-param.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
			if assert.Len(t, status.Nodes, 5) {
				nodeStatus := status.Nodes.FindByDisplayName("sleep")
				assert.Equal(t, wfv1.NodeSkipped, nodeStatus.Phase)
				assert.Equal(t, "Skipped, empty params", nodeStatus.Message)
			}
		})
}

// 128M is for argo executor
func (s *FunctionalSuite) TestPendingRetryWorkflow() {
	s.Given().
		Workflow(`
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: pending-retry-workflow-
  labels:
    argo-e2e: true
spec:
  entrypoint: dag
  templates:
  - name: cowsay
    container:
      image: argoproj/argosay:v2
      args: ["echo", "a"]
      resources:
        limits:
          memory: 128M
  - name: dag
    dag:
      tasks:
      - name: a
        template: cowsay
      - name: b
        template: cowsay
`).
		When().
		MemoryQuota("130M").
		SubmitWorkflow().
		WaitForWorkflow(fixtures.ToStart, "to start").
		WaitForWorkflow(fixtures.Condition(func(wf *wfv1.Workflow) bool {
			a := wf.Status.Nodes.FindByDisplayName("a")
			b := wf.Status.Nodes.FindByDisplayName("b")
			return wfv1.NodePending == a.Phase && wfv1.NodePending == b.Phase
		}), "pods pending").
		DeleteMemoryQuota().
		WaitForWorkflow(fixtures.Condition(func(wf *wfv1.Workflow) bool {
			a := wf.Status.Nodes.FindByDisplayName("a")
			b := wf.Status.Nodes.FindByDisplayName("b")
			return wfv1.NodeSucceeded == a.Phase && wfv1.NodeSucceeded == b.Phase
		}), "pods succeeded")
}

// 128M is for argo executor
func (s *FunctionalSuite) TestPendingRetryWorkflowWithRetryStrategy() {
	s.Given().
		Workflow(`
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: pending-retry-workflow-with-retry-strategy-
  labels:
    argo-e2e: true
spec:
  entrypoint: dag
  templates:
  - name: cowsay
    retryStrategy:
      limit: 1
    container:
      image: argoproj/argosay:v2
      args: ["echo", "a"]
      resources:
        limits:
          memory: 128M
  - name: dag
    dag:
      tasks:
      - name: a
        template: cowsay
      - name: b
        template: cowsay
`).
		When().
		MemoryQuota("130M").
		SubmitWorkflow().
		WaitForWorkflow(fixtures.ToStart, "to start").
		WaitForWorkflow(fixtures.Condition(func(wf *wfv1.Workflow) bool {
			a := wf.Status.Nodes.FindByDisplayName("a(0)")
			b := wf.Status.Nodes.FindByDisplayName("b(0)")
			return wfv1.NodePending == a.Phase && wfv1.NodePending == b.Phase
		}), "pods pending").
		DeleteMemoryQuota().
		WaitForWorkflow(fixtures.Condition(func(wf *wfv1.Workflow) bool {
			a := wf.Status.Nodes.FindByDisplayName("a(0)")
			b := wf.Status.Nodes.FindByDisplayName("b(0)")
			return wfv1.NodeSucceeded == a.Phase && wfv1.NodeSucceeded == b.Phase
		}), "pods succeeded")
}

func (s *FunctionalSuite) TestParameterAggregation() {
	s.Need(fixtures.BaseLayerArtifacts)
	s.Given().
		Workflow("@functional/param-aggregation.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow(60 * time.Second).
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
			nodeStatus := status.Nodes.FindByDisplayName("print(0:res:1)")
			if assert.NotNil(t, nodeStatus) {
				assert.Equal(t, wfv1.NodeSucceeded, nodeStatus.Phase)
			}
		})
}

func (s *FunctionalSuite) TestDAGDepends() {
	s.Given().
		Workflow("@functional/dag-depends.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow(45 * time.Second).
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
			nodeStatus := status.Nodes.FindByDisplayName("A")
			assert.NotNil(t, nodeStatus)
			assert.Equal(t, wfv1.NodeSucceeded, nodeStatus.Phase)
			nodeStatus = status.Nodes.FindByDisplayName("B")
			assert.NotNil(t, nodeStatus)
			assert.Equal(t, wfv1.NodeSucceeded, nodeStatus.Phase)
			nodeStatus = status.Nodes.FindByDisplayName("C")
			assert.NotNil(t, nodeStatus)
			assert.Equal(t, wfv1.NodeFailed, nodeStatus.Phase)
			nodeStatus = status.Nodes.FindByDisplayName("should-execute-1")
			assert.NotNil(t, nodeStatus)
			assert.Equal(t, wfv1.NodeSucceeded, nodeStatus.Phase)
			nodeStatus = status.Nodes.FindByDisplayName("should-execute-2")
			assert.NotNil(t, nodeStatus)
			assert.Equal(t, wfv1.NodeSucceeded, nodeStatus.Phase)
			nodeStatus = status.Nodes.FindByDisplayName("should-not-execute")
			assert.NotNil(t, nodeStatus)
			assert.Equal(t, wfv1.NodeOmitted, nodeStatus.Phase)
			nodeStatus = status.Nodes.FindByDisplayName("should-execute-3")
			assert.NotNil(t, nodeStatus)
			assert.Equal(t, wfv1.NodeSucceeded, nodeStatus.Phase)
		})
}

func (s *FunctionalSuite) TestOptionalInputArtifacts() {
	s.Given().
		Workflow("@testdata/input-artifacts.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
		})
}

func (s *FunctionalSuite) TestWorkflowTemplateRefWithExitHandler() {
	s.Given().
		WorkflowTemplate("@smoke/workflow-template-whalesay-template.yaml").
		Workflow("@testdata/workflow-template-ref-exithandler.yaml").
		When().
		CreateWorkflowTemplates().
		Wait(1 * time.Second). // allow the template to reach the informer
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
			assert.Empty(t, status.Message)
		})
}

func (s *FunctionalSuite) TestParametrizableAds() {
	s.Given().
		Workflow(`
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  name: param-ads
  labels:
    argo-e2e: true
spec:
  entrypoint: whalesay
  arguments:
    parameters:
      - name: ads
        value: "5"
  templates:
  - name: whalesay
    inputs:
      parameters:
        - name: ads
    activeDeadlineSeconds: "{{inputs.parameters.ads}}"
    container:
      image: argoproj/argosay:v2
      args: [sleep, 10s]
`).
		When().
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowFailed, status.Phase)
			if node := status.Nodes.FindByDisplayName("param-ads"); assert.NotNil(t, node) {
				assert.Contains(t, node.Message, "Pod was active on the node longer than the specified deadline")
			}
		})
}

func (s *FunctionalSuite) TestParametrizableLimit() {
	s.Given().
		Workflow(`
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  name: param-limit
  labels:
    argo-e2e: true
spec:
  entrypoint: whalesay
  arguments:
    parameters:
      - name: limit
        value: "1"
  templates:
  - name: whalesay
    inputs:
      parameters:
        - name: limit
    retryStrategy:
      limit: "{{inputs.parameters.limit}}"
    container:
      image: argoproj/argosay:v2
      args: [exit, 1]
`).
		When().
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowFailed, status.Phase)
			if node := status.Nodes.FindByDisplayName("param-limit"); assert.NotNil(t, node) {
				assert.Contains(t, node.Message, "No more retries left")
			}
			assert.Len(t, status.Nodes, 3)
		})
}

// invalid commands will cause the executor to exit earlier than expected, but these errors must still
// get returned to the controller, the wait container must fail the wait
func (s *FunctionalSuite) TestInvalidCommand() {
	s.Given().
		Workflow(`
metadata:
  generateName: invalid-command-
  labels:
    argo-e2e: true
spec:
  entrypoint: main
  templates:
  - name: main
    container:
      image: argoproj/argosay:v2
      command: [invalid-command]
`).
		When().
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowFailed, status.Phase)
			assert.Contains(t, status.Message, "invalid-command")
		})
}

func (s *FunctionalSuite) TestStorageQuotaLimit() {
	// TODO Test fails due to unstable PVC creation and termination in K3S
	// PVC will stuck in pending state for while.

	s.T().SkipNow()
	s.Given().
		Workflow("@testdata/storage-limit.yaml").
		When().
		StorageQuota("5Mi").
		SubmitWorkflow().
		WaitForWorkflow(fixtures.ToStart, "to start").
		WaitForWorkflow(fixtures.Condition(func(wf *wfv1.Workflow) bool {
			return strings.Contains(wf.Status.Message, "Waiting for a PVC to be created")
		}), "PVC pending").
		DeleteStorageQuota().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, _ *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
		})
}

func (s *FunctionalSuite) TestTemplateLevelTimeout() {
	s.Given().
		Workflow(`
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  name: steps-tmpl-timeout
  labels:
    argo-e2e: true
spec:
  entrypoint: hello-hello-hello
  templates:
  - name: hello-hello-hello
    steps:
    - - name: hello1
        template: whalesay
        arguments:
          parameters: [{name: message, value: "5s"}]
      - name: hello2a
        template: whalesay
        arguments:
          parameters: [{name: message, value: "10s"}]
      - name: hello2b
        template: whalesay
        arguments:
          parameters: [{name: message, value: "15s"}]

  - name: whalesay
    timeout: "{{inputs.parameters.message}}"
    inputs:
      parameters:
      - name: message
    container:
      image: argoproj/argosay:v2
      args: [sleep, 30s]
`).
		When().
		SubmitWorkflow().
		WaitForWorkflow(fixtures.Condition(func(wf *wfv1.Workflow) bool {
			return wf.Status.Phase == wfv1.WorkflowFailed
		}), "Waiting for timeout", 30*time.Second)
}

func (s *FunctionalSuite) TestTemplateLevelTimeoutWithForbidden() {
	s.Given().
		Workflow(`
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  name: steps-tmpl-timeout
  labels:
    argo-e2e: true
spec:
  entrypoint: hello-hello-hello
  templates:
  - name: hello-hello-hello
    steps:
    - - name: hello1
        template: whalesay
        arguments:
          parameters: [{name: message, value: "5s"}]
      - name: hello2a
        template: whalesay
        arguments:
          parameters: [{name: message, value: "10s"}]
      - name: hello2b
        template: whalesay
        arguments:
          parameters: [{name: message, value: "15s"}]

  - name: whalesay
    resources:
      limits:
        memory: 145M
    timeout: "{{inputs.parameters.message}}"
    inputs:
      parameters:
      - name: message
    container:
      image: argoproj/argosay:v2
      args: [sleep, 30s]
`).
		When().
		MemoryQuota("130M").
		SubmitWorkflow().
		WaitForWorkflow(fixtures.Condition(func(wf *wfv1.Workflow) bool {
			return wf.Status.Phase == wfv1.WorkflowFailed
		}), "Waiting for timeout", 30*time.Second).
		DeleteMemoryQuota()
}

func (s *FunctionalSuite) TestExitCodePNSSleep() {
	s.Given().
		Workflow(`apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  name: cond
  labels:
    argo-e2e: true
spec:
  entrypoint: conditional-example
  templates:
  - name: conditional-example
    steps:
    - - name: print-hello
        template: whalesay
  - name: whalesay
    container:
      image: argoproj/argosay:v2
      args: [sleep, 5s]
`).
		When().
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, metadata *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			node := status.Nodes.FindByDisplayName("print-hello")
			if assert.NotNil(t, node) && assert.NotNil(t, node.Outputs) && assert.NotNil(t, node.Outputs.ExitCode) {
				assert.Equal(t, "0", *node.Outputs.ExitCode)
			}
		})
}

func (s *FunctionalSuite) TestK8SJSONPatch() {
	s.Given().
		Workflow("@functional/k8s-patch.yaml").
		When().
		SubmitWorkflow().
		WaitForWorkflow().
		Then().
		ExpectWorkflow(func(t *testing.T, metadata *metav1.ObjectMeta, status *wfv1.WorkflowStatus) {
			assert.Equal(t, wfv1.WorkflowSucceeded, status.Phase)
		})
}

func TestFunctionalSuite(t *testing.T) {
	suite.Run(t, new(FunctionalSuite))
}
