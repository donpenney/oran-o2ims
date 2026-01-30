/*
SPDX-FileCopyrightText: Red Hat

SPDX-License-Identifier: Apache-2.0
*/

/*
Test Cases for Metal3 NodeAllocationRequest Controller

This test suite covers the Metal3 hardware plugin's NodeAllocationRequest controller,
focusing on the new timeout handling implementation that was moved from the O-Cloud Manager.

Key Test Areas:
1. checkHardwareTimeout function - Core timeout detection logic
2. HardwareProvisioningTimeout field handling
3. Day 2 retry scenarios with spec changes
4. Callback integration for timeout notifications
5. Integration with HardwareOperationStartTime
*/

package controller

import (
	"context"
	"log/slog"
	"os"
	"time"

	metal3v1alpha1 "github.com/metal3-io/baremetal-operator/apis/metal3.io/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	pluginsv1alpha1 "github.com/openshift-kni/oran-o2ims/api/hardwaremanagement/plugins/v1alpha1"
	hwmgmtv1alpha1 "github.com/openshift-kni/oran-o2ims/api/hardwaremanagement/v1alpha1"
	hwmgrutils "github.com/openshift-kni/oran-o2ims/hwmgr-plugins/controller/utils"
)

var _ = Describe("Metal3 NodeAllocationRequest Controller Timeout Handling", func() {
	var (
		c          client.Client
		reconciler *NodeAllocationRequestReconciler
		logger     *slog.Logger
	)

	BeforeEach(func() {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

		scheme := runtime.NewScheme()
		Expect(pluginsv1alpha1.AddToScheme(scheme)).To(Succeed())

		c = fake.NewClientBuilder().WithScheme(scheme).Build()

		reconciler = &NodeAllocationRequestReconciler{
			Client:          c,
			NoncachedClient: c,
			Logger:          logger,
		}
	})

	Describe("checkHardwareTimeout", func() {
		var nar *pluginsv1alpha1.NodeAllocationRequest

		BeforeEach(func() {
			nar = &pluginsv1alpha1.NodeAllocationRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nar",
					Namespace: "default",
				},
				Spec: pluginsv1alpha1.NodeAllocationRequestSpec{
					HardwareProvisioningTimeout: "5m",
				},
				Status: pluginsv1alpha1.NodeAllocationRequestStatus{
					Conditions: []metav1.Condition{},
				},
			}
		})

		Context("when HardwareProvisioningTimeout is specified", func() {
			It("should use the specified timeout value", func() {
				nar.Spec.HardwareProvisioningTimeout = "10m"
				timeoutExceeded, conditionType, err := reconciler.checkHardwareTimeout(nar)
				Expect(err).ToNot(HaveOccurred())
				Expect(timeoutExceeded).To(BeFalse())
				Expect(conditionType).To(Equal(hwmgmtv1alpha1.ConditionType("")))
			})
		})

		Context("when HardwareProvisioningTimeout is empty", func() {
			It("should use default timeout", func() {
				nar.Spec.HardwareProvisioningTimeout = ""
				timeoutExceeded, conditionType, err := reconciler.checkHardwareTimeout(nar)
				Expect(err).ToNot(HaveOccurred())
				Expect(timeoutExceeded).To(BeFalse())
				Expect(conditionType).To(Equal(hwmgmtv1alpha1.ConditionType("")))
			})
		})

		Context("when HardwareProvisioningTimeout is invalid", func() {
			It("should return error for invalid duration", func() {
				nar.Spec.HardwareProvisioningTimeout = "invalid"
				timeoutExceeded, conditionType, err := reconciler.checkHardwareTimeout(nar)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid hardware provisioning timeout"))
				Expect(timeoutExceeded).To(BeFalse())
				Expect(conditionType).To(Equal(hwmgmtv1alpha1.ConditionType("")))
			})

			It("should return error for zero timeout", func() {
				nar.Spec.HardwareProvisioningTimeout = "0s"
				timeoutExceeded, conditionType, err := reconciler.checkHardwareTimeout(nar)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("hardware provisioning timeout must be > 0"))
				Expect(timeoutExceeded).To(BeFalse())
				Expect(conditionType).To(Equal(hwmgmtv1alpha1.ConditionType("")))
			})
		})

		Context("when provisioning is in progress and times out", func() {
			BeforeEach(func() {
				// Set operation start time to 10 minutes ago (exceeds 5m timeout)
				startTime := metav1.Time{Time: time.Now().Add(-10 * time.Minute)}
				nar.Status.HardwareOperationStartTime = &startTime

				// Add provisioning condition in progress
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Provisioned),
					string(hwmgmtv1alpha1.InProgress),
					metav1.ConditionFalse,
					"Hardware provisioning in progress")
			})

			It("should detect provisioning timeout", func() {
				timeoutExceeded, conditionType, err := reconciler.checkHardwareTimeout(nar)
				Expect(err).ToNot(HaveOccurred())
				Expect(timeoutExceeded).To(BeTrue())
				Expect(conditionType).To(Equal(hwmgmtv1alpha1.Provisioned))
			})
		})

		Context("when provisioning is in progress but not timed out", func() {
			BeforeEach(func() {
				// Set operation start time to 2 minutes ago (within 5m timeout)
				startTime := metav1.Time{Time: time.Now().Add(-2 * time.Minute)}
				nar.Status.HardwareOperationStartTime = &startTime

				// Add provisioning condition in progress
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Provisioned),
					string(hwmgmtv1alpha1.InProgress),
					metav1.ConditionFalse,
					"Hardware provisioning in progress")
			})

			It("should not detect timeout", func() {
				timeoutExceeded, conditionType, err := reconciler.checkHardwareTimeout(nar)
				Expect(err).ToNot(HaveOccurred())
				Expect(timeoutExceeded).To(BeFalse())
				Expect(conditionType).To(Equal(hwmgmtv1alpha1.ConditionType("")))
			})
		})

		Context("when configuration is in progress and times out", func() {
			BeforeEach(func() {
				// Set provisioning as completed
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Provisioned),
					string(hwmgmtv1alpha1.Completed),
					metav1.ConditionTrue,
					"Hardware provisioning completed")

				// Set operation start time to 10 minutes ago (exceeds 5m timeout)
				startTime := metav1.Time{Time: time.Now().Add(-10 * time.Minute)}
				nar.Status.HardwareOperationStartTime = &startTime

				// Add configuration condition in progress
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Configured),
					string(hwmgmtv1alpha1.InProgress),
					metav1.ConditionFalse,
					"Hardware configuration in progress")
			})

			It("should detect configuration timeout", func() {
				timeoutExceeded, conditionType, err := reconciler.checkHardwareTimeout(nar)
				Expect(err).ToNot(HaveOccurred())
				Expect(timeoutExceeded).To(BeTrue())
				Expect(conditionType).To(Equal(hwmgmtv1alpha1.Configured))
			})
		})

		Context("when configuration is in progress but not timed out", func() {
			BeforeEach(func() {
				// Set provisioning as completed
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Provisioned),
					string(hwmgmtv1alpha1.Completed),
					metav1.ConditionTrue,
					"Hardware provisioning completed")

				// Set operation start time to 2 minutes ago (within 5m timeout)
				startTime := metav1.Time{Time: time.Now().Add(-2 * time.Minute)}
				nar.Status.HardwareOperationStartTime = &startTime

				// Add configuration condition in progress
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Configured),
					string(hwmgmtv1alpha1.InProgress),
					metav1.ConditionFalse,
					"Hardware configuration in progress")
			})

			It("should not detect timeout", func() {
				timeoutExceeded, conditionType, err := reconciler.checkHardwareTimeout(nar)
				Expect(err).ToNot(HaveOccurred())
				Expect(timeoutExceeded).To(BeFalse())
				Expect(conditionType).To(Equal(hwmgmtv1alpha1.ConditionType("")))
			})
		})

		Context("when both provisioning and configuration are completed", func() {
			BeforeEach(func() {
				// Set both conditions as completed
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Provisioned),
					string(hwmgmtv1alpha1.Completed),
					metav1.ConditionTrue,
					"Hardware provisioning completed")

				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Configured),
					string(hwmgmtv1alpha1.Completed),
					metav1.ConditionTrue,
					"Hardware configuration completed")
			})

			It("should not detect any timeout", func() {
				timeoutExceeded, conditionType, err := reconciler.checkHardwareTimeout(nar)
				Expect(err).ToNot(HaveOccurred())
				Expect(timeoutExceeded).To(BeFalse())
				Expect(conditionType).To(Equal(hwmgmtv1alpha1.ConditionType("")))
			})
		})

		Context("when provisioning is in progress but HardwareOperationStartTime is missing", func() {
			BeforeEach(func() {
				// Add provisioning condition in progress but no start time
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Provisioned),
					string(hwmgmtv1alpha1.InProgress),
					metav1.ConditionFalse,
					"Hardware provisioning in progress")
			})

			It("should not detect timeout without start time", func() {
				timeoutExceeded, conditionType, err := reconciler.checkHardwareTimeout(nar)
				Expect(err).ToNot(HaveOccurred())
				Expect(timeoutExceeded).To(BeFalse())
				Expect(conditionType).To(Equal(hwmgmtv1alpha1.ConditionType("")))
			})
		})

		Context("when configuration is in progress but HardwareOperationStartTime is missing", func() {
			BeforeEach(func() {
				// Set provisioning as completed
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Provisioned),
					string(hwmgmtv1alpha1.Completed),
					metav1.ConditionTrue,
					"Hardware provisioning completed")

				// Add configuration condition in progress but no start time
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Configured),
					string(hwmgmtv1alpha1.InProgress),
					metav1.ConditionFalse,
					"Hardware configuration in progress")
			})

			It("should not detect timeout without start time", func() {
				timeoutExceeded, conditionType, err := reconciler.checkHardwareTimeout(nar)
				Expect(err).ToNot(HaveOccurred())
				Expect(timeoutExceeded).To(BeFalse())
				Expect(conditionType).To(Equal(hwmgmtv1alpha1.ConditionType("")))
			})
		})
	})

	Describe("Day 2 retry scenarios", func() {
		var nar *pluginsv1alpha1.NodeAllocationRequest

		BeforeEach(func() {
			nar = &pluginsv1alpha1.NodeAllocationRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nar-day2",
					Namespace: "default",
				},
				Spec: pluginsv1alpha1.NodeAllocationRequestSpec{
					HardwareProvisioningTimeout: "5m",
					ConfigTransactionId:         2, // Indicates spec change
				},
				Status: pluginsv1alpha1.NodeAllocationRequestStatus{
					Conditions: []metav1.Condition{},
				},
			}
		})

		Context("when configuration failed and spec changed", func() {
			BeforeEach(func() {
				// Set provisioning as completed
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Provisioned),
					string(hwmgmtv1alpha1.Completed),
					metav1.ConditionTrue,
					"Hardware provisioning completed")

				// Set configuration as failed
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Configured),
					string(hwmgmtv1alpha1.Failed),
					metav1.ConditionFalse,
					"Hardware configuration failed")

				// Set operation start time to old (exceeded timeout) - this should be ignored when spec changes
				startTime := metav1.Time{Time: time.Now().Add(-10 * time.Minute)}
				nar.Status.HardwareOperationStartTime = &startTime

				// Set ObservedConfigTransactionId to 1, but Spec.ConfigTransactionId is 2 (mismatch = spec change)
				nar.Status.ObservedConfigTransactionId = 1
			})

			It("should allow retry when spec changes", func() {
				// The Metal3 controller should detect the spec change and skip timeout checking
				// This allows retry even when the previous configuration failed/timed out
				timeoutExceeded, conditionType, err := reconciler.checkHardwareTimeout(nar)
				Expect(err).ToNot(HaveOccurred())
				Expect(timeoutExceeded).To(BeFalse())
				Expect(conditionType).To(Equal(hwmgmtv1alpha1.ConditionType("")))
			})
		})

		Context("when configuration timed out and spec changed", func() {
			BeforeEach(func() {
				// Set provisioning as completed
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Provisioned),
					string(hwmgmtv1alpha1.Completed),
					metav1.ConditionTrue,
					"Hardware provisioning completed")

				// Set configuration as timed out
				hwmgrutils.SetStatusCondition(&nar.Status.Conditions,
					string(hwmgmtv1alpha1.Configured),
					string(hwmgmtv1alpha1.TimedOut),
					metav1.ConditionFalse,
					"Hardware configuration timed out")

				// Set operation start time to old (exceeded timeout) - this should be ignored when spec changes
				startTime := metav1.Time{Time: time.Now().Add(-10 * time.Minute)}
				nar.Status.HardwareOperationStartTime = &startTime

				// Set ObservedConfigTransactionId to 1, but Spec.ConfigTransactionId is 2 (mismatch = spec change)
				nar.Status.ObservedConfigTransactionId = 1
			})

			It("should allow retry when spec changes", func() {
				// Similar to failed case, should allow retry with spec change
				// Timeout check should be skipped when spec changes
				timeoutExceeded, conditionType, err := reconciler.checkHardwareTimeout(nar)
				Expect(err).ToNot(HaveOccurred())
				Expect(timeoutExceeded).To(BeFalse())
				Expect(conditionType).To(Equal(hwmgmtv1alpha1.ConditionType("")))
			})
		})
	})

	// Integration-style test for firmware spec cleanup on timeout
	// Note: This uses a fake client, so it doesn't fully test the reconciliation loop,
	// but it verifies the cleanup logic is called correctly
	Describe("Configuration timeout firmware spec cleanup", Ordered, func() {
		var (
			ctx        context.Context
			nar        *pluginsv1alpha1.NodeAllocationRequest
			node1      *pluginsv1alpha1.AllocatedNode
			node2      *pluginsv1alpha1.AllocatedNode
			bmh1       *metal3v1alpha1.BareMetalHost
			bmh2       *metal3v1alpha1.BareMetalHost
			hfc1       *metal3v1alpha1.HostFirmwareComponents
			hfc2       *metal3v1alpha1.HostFirmwareComponents
			hfs1       *metal3v1alpha1.HostFirmwareSettings
			hfs2       *metal3v1alpha1.HostFirmwareSettings
			testClient client.Client
		)

		BeforeAll(func() {
			ctx = context.Background()

			// Create scheme with all types
			scheme := runtime.NewScheme()
			Expect(pluginsv1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(metal3v1alpha1.AddToScheme(scheme)).To(Succeed())

			// Create NAR that has timed out during configuration
			nar = &pluginsv1alpha1.NodeAllocationRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nar-timeout",
					Namespace: "test-ns",
				},
				Spec: pluginsv1alpha1.NodeAllocationRequestSpec{
					HardwareProvisioningTimeout: "5m",
				},
				Status: pluginsv1alpha1.NodeAllocationRequestStatus{
					// Set start time to 10 minutes ago (timed out)
					HardwareOperationStartTime: &metav1.Time{Time: time.Now().Add(-10 * time.Minute)},
					Conditions: []metav1.Condition{
						{
							Type:   string(hwmgmtv1alpha1.Provisioned),
							Status: metav1.ConditionTrue,
							Reason: string(hwmgmtv1alpha1.Completed),
						},
						{
							Type:   string(hwmgmtv1alpha1.Configured),
							Status: metav1.ConditionFalse,
							Reason: string(hwmgmtv1alpha1.InProgress),
						},
					},
				},
			}

			// Create two nodes
			node1 = &pluginsv1alpha1.AllocatedNode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node1",
					Namespace: "test-ns",
				},
				Spec: pluginsv1alpha1.AllocatedNodeSpec{
					NodeAllocationRequest: "test-nar-timeout",
					HwMgrNodeId:           "bmh1",
					HwMgrNodeNs:           "test-ns",
				},
			}

			node2 = &pluginsv1alpha1.AllocatedNode{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node2",
					Namespace: "test-ns",
				},
				Spec: pluginsv1alpha1.AllocatedNodeSpec{
					NodeAllocationRequest: "test-nar-timeout",
					HwMgrNodeId:           "bmh2",
					HwMgrNodeNs:           "test-ns",
				},
			}

			// Create BMHs
			bmh1 = &metal3v1alpha1.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bmh1",
					Namespace: "test-ns",
				},
			}

			bmh2 = &metal3v1alpha1.BareMetalHost{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bmh2",
					Namespace: "test-ns",
				},
			}

			// Create HFCs with spec.updates
			hfc1 = &metal3v1alpha1.HostFirmwareComponents{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bmh1",
					Namespace: "test-ns",
				},
				Spec: metal3v1alpha1.HostFirmwareComponentsSpec{
					Updates: []metal3v1alpha1.FirmwareUpdate{
						{Component: "bios", URL: "http://example.com/bios1.bin"},
					},
				},
			}

			hfc2 = &metal3v1alpha1.HostFirmwareComponents{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bmh2",
					Namespace: "test-ns",
				},
				Spec: metal3v1alpha1.HostFirmwareComponentsSpec{
					Updates: []metal3v1alpha1.FirmwareUpdate{
						{Component: "bios", URL: "http://example.com/bios2.bin"},
					},
				},
			}

			// Create HFSs with spec.settings
			hfs1 = &metal3v1alpha1.HostFirmwareSettings{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bmh1",
					Namespace: "test-ns",
				},
				Spec: metal3v1alpha1.HostFirmwareSettingsSpec{
					Settings: map[string]intstr.IntOrString{
						"ProcTurboMode": intstr.FromString("Enabled"),
					},
				},
			}

			hfs2 = &metal3v1alpha1.HostFirmwareSettings{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "bmh2",
					Namespace: "test-ns",
				},
				Spec: metal3v1alpha1.HostFirmwareSettingsSpec{
					Settings: map[string]intstr.IntOrString{
						"BootMode": intstr.FromString("UEFI"),
					},
				},
			}

			// Create client with all objects
			testClient = fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(nar, node1, node2, bmh1, bmh2, hfc1, hfc2, hfs1, hfs2).
				WithStatusSubresource(&pluginsv1alpha1.NodeAllocationRequest{}).
				WithIndex(&pluginsv1alpha1.AllocatedNode{}, "spec.nodeAllocationRequest", func(o client.Object) []string {
					node := o.(*pluginsv1alpha1.AllocatedNode)
					return []string{node.Spec.NodeAllocationRequest}
				}).
				Build()
		})

		It("should clear firmware specs for all nodes when configuration times out", func() {
			// Verify firmware specs exist before timeout
			updatedHFC1 := &metal3v1alpha1.HostFirmwareComponents{}
			Expect(testClient.Get(ctx, types.NamespacedName{Name: "bmh1", Namespace: "test-ns"}, updatedHFC1)).To(Succeed())
			Expect(updatedHFC1.Spec.Updates).NotTo(BeEmpty(), "HFC1 should have updates before cleanup")

			// Call the cleanup function directly (simulating what happens in timeout handler)
			err := clearFirmwareSpecFieldsForNAR(ctx, testClient, logger, nar)
			Expect(err).ToNot(HaveOccurred())

			// Verify firmware specs were cleared for both nodes
			updatedHFC1 = &metal3v1alpha1.HostFirmwareComponents{}
			Expect(testClient.Get(ctx, types.NamespacedName{Name: "bmh1", Namespace: "test-ns"}, updatedHFC1)).To(Succeed())
			Expect(updatedHFC1.Spec.Updates).To(BeEmpty(), "HFC1 updates should be cleared after timeout")

			updatedHFC2 := &metal3v1alpha1.HostFirmwareComponents{}
			Expect(testClient.Get(ctx, types.NamespacedName{Name: "bmh2", Namespace: "test-ns"}, updatedHFC2)).To(Succeed())
			Expect(updatedHFC2.Spec.Updates).To(BeEmpty(), "HFC2 updates should be cleared after timeout")

			updatedHFS1 := &metal3v1alpha1.HostFirmwareSettings{}
			Expect(testClient.Get(ctx, types.NamespacedName{Name: "bmh1", Namespace: "test-ns"}, updatedHFS1)).To(Succeed())
			Expect(updatedHFS1.Spec.Settings).To(BeEmpty(), "HFS1 settings should be cleared after timeout")

			updatedHFS2 := &metal3v1alpha1.HostFirmwareSettings{}
			Expect(testClient.Get(ctx, types.NamespacedName{Name: "bmh2", Namespace: "test-ns"}, updatedHFS2)).To(Succeed())
			Expect(updatedHFS2.Spec.Settings).To(BeEmpty(), "HFS2 settings should be cleared after timeout")
		})
	})
})
