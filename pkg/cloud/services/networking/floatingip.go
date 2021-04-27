/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package networking

import (
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/layer3/floatingips"
	"k8s.io/apimachinery/pkg/util/wait"

	infrav1 "sigs.k8s.io/cluster-api-provider-openstack/api/v1alpha4"
	"sigs.k8s.io/cluster-api-provider-openstack/pkg/record"
)

func (s *Service) GetOrCreateFloatingIP(openStackCluster *infrav1.OpenStackCluster, ip string) (*floatingips.FloatingIP, error) {
	var fp *floatingips.FloatingIP
	var err error
	var fpCreateOpts floatingips.CreateOpts

	if ip != "" {
		fp, err = checkIfFloatingIPExists(s.client, ip)
		if err != nil {
			return nil, err
		}
		if fp != nil {
			return fp, nil
		}
		// only admin can add ip address
		fpCreateOpts.FloatingIP = ip
	}

	fpCreateOpts.FloatingNetworkID = openStackCluster.Status.ExternalNetwork.ID

	fp, err = floatingips.Create(s.client, fpCreateOpts).Extract()
	if err != nil {
		record.Warnf(openStackCluster, "FailedCreateFloatingIP", "Failed to create floating IP %s: %v", ip, err)
		return nil, err
	}

	record.Eventf(openStackCluster, "SuccessfulCreateFloatingIP", "Created floating IP %s with id %s", fp.FloatingIP, fp.ID)
	return fp, nil
}

func checkIfFloatingIPExists(client *gophercloud.ServiceClient, ip string) (*floatingips.FloatingIP, error) {
	allPages, err := floatingips.List(client, floatingips.ListOpts{FloatingIP: ip}).AllPages()
	if err != nil {
		return nil, err
	}
	fpList, err := floatingips.ExtractFloatingIPs(allPages)
	if err != nil {
		return nil, err
	}
	if len(fpList) == 0 {
		return nil, nil
	}
	return &fpList[0], nil
}

func (s *Service) GetFloatingIPByPortID(portID string) (*floatingips.FloatingIP, error) {
	allPages, err := floatingips.List(s.client, floatingips.ListOpts{PortID: portID}).AllPages()
	if err != nil {
		return nil, err
	}
	fpList, err := floatingips.ExtractFloatingIPs(allPages)
	if err != nil {
		return nil, err
	}
	if len(fpList) == 0 {
		return nil, nil
	}
	return &fpList[0], nil
}

func (s *Service) DeleteFloatingIP(openStackCluster *infrav1.OpenStackCluster, ip string) error {
	fip, err := checkIfFloatingIPExists(s.client, ip)
	if err != nil {
		return err
	}
	if fip == nil {
		// nothing to do
		return nil
	}

	if err = floatingips.Delete(s.client, fip.ID).ExtractErr(); err != nil {
		record.Warnf(openStackCluster, "FailedDeleteFloatingIP", "Failed to delete floating IP %s: %v", ip, err)
		return err
	}

	record.Eventf(openStackCluster, "SuccessfulDeleteFloatingIP", "Deleted floating IP %s", ip)
	return nil
}

var backoff = wait.Backoff{
	Steps:    10,
	Duration: 30 * time.Second,
	Factor:   1.0,
	Jitter:   0.1,
}

func (s *Service) AssociateFloatingIP(openStackCluster *infrav1.OpenStackCluster, fp *floatingips.FloatingIP, portID string) error {
	s.logger.Info("Associating floating IP", "id", fp.ID, "ip", fp.FloatingIP)
	fpUpdateOpts := &floatingips.UpdateOpts{
		PortID: &portID,
	}
	_, err := floatingips.Update(s.client, fp.ID, fpUpdateOpts).Extract()
	if err != nil {
		record.Warnf(openStackCluster, "FailedAssociateFloatingIP", "Failed to associate floating IP %s with port %s: %v", fp.FloatingIP, portID, err)
		return err
	}
	record.Eventf(openStackCluster, "SuccessfulAssociateFloatingIP", "Associated floating IP %s with port %s", fp.FloatingIP, portID)

	s.logger.Info("Waiting for floating IP", "id", fp.ID, "targetStatus", "ACTIVE")

	return wait.ExponentialBackoff(backoff, func() (bool, error) {
		fp, err := floatingips.Get(s.client, fp.ID).Extract()
		if err != nil {
			return false, err
		}
		return fp.Status == "ACTIVE", nil
	})
}

func (s *Service) DisassociateFloatingIP(openStackCluster *infrav1.OpenStackCluster, ip string) error {
	fip, err := checkIfFloatingIPExists(s.client, ip)
	if err != nil {
		return err
	}
	if fip == nil || fip.FloatingIP == "" {
		s.logger.Info("Floating IP not associated", "ip", ip)
		return nil
	}

	s.logger.Info("Disassociating floating IP", "id", fip.ID, "ip", fip.FloatingIP)

	fpUpdateOpts := &floatingips.UpdateOpts{
		PortID: nil,
	}
	_, err = floatingips.Update(s.client, fip.ID, fpUpdateOpts).Extract()
	if err != nil {
		record.Warnf(openStackCluster, "FailedDisassociateFloatingIP", "Failed to disassociate floating IP %s: %v", fip.FloatingIP, err)
		return err
	}
	record.Eventf(openStackCluster, "SuccessfulDisassociateFloatingIP", "Disassociated floating IP %s", fip.FloatingIP)

	s.logger.Info("Waiting for floating IP", "id", fip.ID, "targetStatus", "DOWN")

	return wait.ExponentialBackoff(backoff, func() (bool, error) {
		fip, err := floatingips.Get(s.client, fip.ID).Extract()
		if err != nil {
			return false, err
		}
		return fip.Status == "DOWN", nil
	})
}
