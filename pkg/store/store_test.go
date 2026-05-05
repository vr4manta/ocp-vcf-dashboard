package store

import (
	"testing"
	"time"

	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/prow"
	"github.com/jcpowermac/ocp-vcf-dashboard/pkg/vcenter"
	vcmv1 "github.com/openshift-splat-team/vsphere-capacity-manager/pkg/apis/vspherecapacitymanager.splat.io/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestStorePool(t *testing.T) {
	s := New()

	pool := &vcmv1.Pool{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pool"},
	}

	s.SetPool(pool)
	got := s.GetPool("test-pool")
	if got == nil || got.Name != "test-pool" {
		t.Errorf("expected pool 'test-pool', got %v", got)
	}

	pools := s.GetPools()
	if len(pools) != 1 {
		t.Errorf("expected 1 pool, got %d", len(pools))
	}

	s.DeletePool("test-pool")
	if s.GetPool("test-pool") != nil {
		t.Errorf("expected pool to be deleted")
	}
}

func TestStoreLease(t *testing.T) {
	s := New()

	lease := &vcmv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: "test-lease"},
	}

	s.SetLease(lease)
	got := s.GetLease("test-lease")
	if got == nil || got.Name != "test-lease" {
		t.Errorf("expected lease 'test-lease', got %v", got)
	}

	s.DeleteLease("test-lease")
	if s.GetLease("test-lease") != nil {
		t.Errorf("expected lease to be deleted")
	}
}

func TestStoreProw(t *testing.T) {
	s := New()

	jobs := []prow.JobSummary{
		{Job: "job-1", LatestState: "success"},
		{Job: "job-2", LatestState: "failure"},
	}

	s.SetProwJobs(jobs)

	gotJobs, updated := s.GetProwJobs()
	if len(gotJobs) != 2 {
		t.Errorf("expected 2 jobs, got %d", len(gotJobs))
	}
	if updated.IsZero() {
		t.Errorf("expected updated time to be set")
	}

	summary := s.GetProwJobSummary("job-1")
	if summary == nil || summary.LatestState != "success" {
		t.Errorf("expected summary for job-1 to have success state")
	}
}

func TestStoreVCenter(t *testing.T) {
	s := New()

	data := &vcenter.VCenterData{
		Server: "vc1",
		VMs: []vcenter.VMInfo{
			{Name: "vm1", ClusterID: "cluster-1", Namespace: "ns-1"},
		},
	}

	s.SetVCenterData("vc1", data)

	vms := s.GetVMsByClusterID("cluster-1")
	if len(vms) != 1 || vms[0].Name != "vm1" {
		t.Errorf("expected 1 VM for cluster-1, got %v", vms)
	}

	nsVMs := s.GetVMsByNamespace("ns-1")
	if len(nsVMs) != 1 || nsVMs[0].Name != "vm1" {
		t.Errorf("expected 1 VM for ns-1, got %v", nsVMs)
	}
}

func TestStoreSubscribe(t *testing.T) {
	s := New()
	ch := s.Subscribe()

	go s.SetPool(&vcmv1.Pool{ObjectMeta: metav1.ObjectMeta{Name: "test"}})

	var events []EventType
	timer := time.NewTimer(1 * time.Second)
	defer timer.Stop()

collect:
	for {
		select {
		case evt := <-ch:
			events = append(events, evt)
			if len(events) >= 2 {
				break collect
			}
		case <-timer.C:
			break collect
		}
	}

	if len(events) < 2 {
		t.Errorf("expected 2 events, got %v", events)
	}

	s.Unsubscribe(ch)
}
