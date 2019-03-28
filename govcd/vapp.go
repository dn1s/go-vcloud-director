/*
 * Copyright 2019 VMware, Inc.  All rights reserved.  Licensed under the Apache v2 License.
 */

package govcd

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/vmware/go-vcloud-director/v2/types/v56"
	"github.com/vmware/go-vcloud-director/v2/util"
)

type VApp struct {
	VApp   *types.VApp
	client *Client
}

func NewVApp(cli *Client) *VApp {
	return &VApp{
		VApp:   new(types.VApp),
		client: cli,
	}
}

func (vdcCli *VCDClient) NewVApp(client *Client) VApp {
	newvapp := NewVApp(client)
	return *newvapp
}

// struct type used to pass information for vApp network creation
type VappNetworkSettings struct {
	Name             string
	Gateway          string
	NetMask          string
	DNS1             string
	DNS2             string
	DNSSuffix        string
	GuestVLANAllowed *bool
	StaticIPRanges   []*types.IPRange
	DhcpSettings     *DhcpSettings
}

// struct type used to pass information for vApp network DHCP
type DhcpSettings struct {
	IsEnabled        bool
	MaxLeaseTime     int
	DefaultLeaseTime int
	IPRange          *types.IPRange
}

// Returns the vdc where the vapp resides in.
func (vapp *VApp) getParentVDC() (Vdc, error) {
	for _, link := range vapp.VApp.Link {
		if link.Type == "application/vnd.vmware.vcloud.vdc+xml" {
			getParentUrl, err := url.ParseRequestURI(link.HREF)
			if err != nil {
				return Vdc{}, fmt.Errorf("Cannot parse HREF : %v", err)
			}
			req := vapp.client.NewRequest(map[string]string{}, "GET", *getParentUrl, nil)
			resp, err := checkResp(vapp.client.Http.Do(req))

			vdc := NewVdc(vapp.client)
			if err = decodeBody(resp, vdc.Vdc); err != nil {
				return Vdc{}, fmt.Errorf("error decoding task response: %s", err)
			}
			return *vdc, nil
		}
	}
	return Vdc{}, fmt.Errorf("Could not find a parent Vdc")
}

func (vapp *VApp) Refresh() error {

	if vapp.VApp.HREF == "" {
		return fmt.Errorf("cannot refresh, Object is empty")
	}

	refreshUrl, _ := url.ParseRequestURI(vapp.VApp.HREF)

	req := vapp.client.NewRequest(map[string]string{}, "GET", *refreshUrl, nil)

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return fmt.Errorf("error retrieving task: %s", err)
	}

	// Empty struct before a new unmarshal, otherwise we end up with duplicate
	// elements in slices.
	vapp.VApp = &types.VApp{}

	if err = decodeBody(resp, vapp.VApp); err != nil {
		return fmt.Errorf("error decoding task response: %s", err)
	}

	// The request was successful
	return nil
}

// Function create vm in vApp using vApp template
// orgVdcNetworks - adds org VDC networks to be available for vApp. Can be empty.
// vappNetworkName - adds vApp network to be available for vApp. Can be empty.
// vappTemplate - vApp Template which will be used for VM creation.
// name - name for VM.
// acceptAllEulas - setting allows to automatically accept or not Eulas.
func (vapp *VApp) AddVM(networks []map[string]interface{}, vappTemplate VAppTemplate, name string, acceptAllEulas bool) (Task, error) {

	if vappTemplate == (VAppTemplate{}) || vappTemplate.VAppTemplate == nil {
		return Task{}, fmt.Errorf("vApp Template can not be empty")
	}

	// Status 8 means The object is resolved and powered off.
	// https://vdc-repo.vmware.com/vmwb-repository/dcr-public/94b8bd8d-74ff-4fe3-b7a4-41ae31516ed7/1b42f3b5-8b31-4279-8b3f-547f6c7c5aa8/doc/GUID-843BE3AD-5EF6-4442-B864-BCAE44A51867.html
	if vappTemplate.VAppTemplate.Status != 8 {
		return Task{}, fmt.Errorf("vApp Template shape is not ok")
	}

	vcomp := &types.ReComposeVAppParams{
		Ovf:         "http://schemas.dmtf.org/ovf/envelope/1",
		Xsi:         "http://www.w3.org/2001/XMLSchema-instance",
		Xmlns:       "http://www.vmware.com/vcloud/v1.5",
		Deploy:      false,
		Name:        vapp.VApp.Name,
		PowerOn:     false,
		Description: vapp.VApp.Description,
		SourcedItem: &types.SourcedCompositionItemParam{
			Source: &types.Reference{
				HREF: vappTemplate.VAppTemplate.Children.VM[0].HREF,
				Name: name,
			},
			VMGeneralParams: &types.VMGeneralParams{
				Name:               name,
				NeedsCustomization: true,
			},
			InstantiationParams: &types.InstantiationParams{
				NetworkConnectionSection: &types.NetworkConnectionSection{
					Info:                          "Network config for sourced item",
					HREF:                          vappTemplate.VAppTemplate.Children.VM[0].NetworkConnectionSection.HREF,
					Type:                          vappTemplate.VAppTemplate.Children.VM[0].NetworkConnectionSection.Type,
					PrimaryNetworkConnectionIndex: vappTemplate.VAppTemplate.Children.VM[0].NetworkConnectionSection.PrimaryNetworkConnectionIndex,
				},
			},
		},
		AllEULAsAccepted: acceptAllEulas,
	}

	for index, network := range networks {
		ipAllocationMode := "NONE"
		ipAddress := "Any"
		if network["ip"].(string) == "dhcp" {
			ipAllocationMode = "DHCP"
		} else if network["ip"].(string) == "allocated" {
			ipAllocationMode = "POOL"
		} else if network["ip"].(string) == "none" {
			ipAllocationMode = "NONE"
		} else if network["ip"].(string) != "" {
			ipAllocationMode = "MANUAL"
			if net.ParseIP(network["ip"].(string)) != nil {
				ipAddress = network["ip"].(string)
			} else {
				ipAllocationMode = "DHCP"
			}
		} else {
			ipAllocationMode = network["ip_allocation_mode"].(string)
		}

		vcomp.SourcedItem.InstantiationParams.NetworkConnectionSection.NetworkConnection = append(vcomp.SourcedItem.InstantiationParams.NetworkConnectionSection.NetworkConnection,
			&types.NetworkConnection{
				Network:                 network["orgnetwork"].(string),
				NetworkConnectionIndex:  index,
				IsConnected:             true,
				IPAddress:               ipAddress,
				IPAddressAllocationMode: ipAllocationMode,
			},
		)

		if network["adapter_type"].(string) != "" {
			vcomp.SourcedItem.InstantiationParams.NetworkConnectionSection.NetworkConnection[index].NetworkAdapterType = network["adapter_type"].(string)
		}

		if network["is_primary"].(bool) == true {
			vcomp.SourcedItem.InstantiationParams.NetworkConnectionSection.PrimaryNetworkConnectionIndex = index
		}

		vcomp.SourcedItem.NetworkAssignment = append(vcomp.SourcedItem.NetworkAssignment,
			&types.NetworkAssignment{
				InnerNetwork:     network["orgnetwork"].(string),
				ContainerNetwork: network["orgnetwork"].(string),
			},
		)
	}

	vcomp.SourcedItem.VMCapabilities = &types.VMCapabilities{
		MemoryHotAddEnabled: true,
		CPUHotAddEnabled:    true,
	}

	output, _ := xml.MarshalIndent(vcomp, "  ", "    ")

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)
	apiEndpoint.Path += "/action/recomposeVApp"

	util.Logger.Printf("[TRACE] Recompose XML: %s", string(output))

	buffer := bytes.NewBufferString(xml.Header + string(output))

	req := vapp.client.NewRequest(map[string]string{}, "POST", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.recomposeVAppParams+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error instantiating a new VM: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding task response: %s", err)
	}

	return *task, nil
}

func (vapp *VApp) RemoveVM(vm VM) error {

	vapp.Refresh()
	task := NewTask(vapp.client)
	if vapp.VApp.Tasks != nil {
		for _, taskItem := range vapp.VApp.Tasks.Task {
			task.Task = taskItem
			err := task.WaitTaskCompletion()
			if err != nil {
				return fmt.Errorf("Error performing task: %#v", err)
			}
		}
	}

	vcomp := &types.ReComposeVAppParams{
		Ovf:   "http://schemas.dmtf.org/ovf/envelope/1",
		Xsi:   "http://www.w3.org/2001/XMLSchema-instance",
		Xmlns: "http://www.vmware.com/vcloud/v1.5",
		DeleteItem: &types.DeleteItem{
			HREF: vm.VM.HREF,
		},
	}

	output, _ := xml.MarshalIndent(vcomp, "  ", "    ")

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)
	apiEndpoint.Path += "/action/recomposeVApp"

	buffer := bytes.NewBufferString(xml.Header + string(output))

	req := vapp.client.NewRequest(map[string]string{}, "POST", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.recomposeVAppParams+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return fmt.Errorf("error instantiating a new vApp: %s", err)
	}

	task = NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return fmt.Errorf("error decoding task response: %s", err)
	}

	err = task.WaitTaskCompletion()
	if err != nil {
		return fmt.Errorf("Error performing task: %#v", err)
	}

	return nil
}

func (vapp *VApp) PowerOn() (Task, error) {

	err := vapp.BlockWhileStatus("UNRESOLVED", vapp.client.MaxRetryTimeout)
	if err != nil {
		return Task{}, fmt.Errorf("error powering on vApp: %s", err)
	}

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)
	apiEndpoint.Path += "/power/action/powerOn"

	req := vapp.client.NewRequest(map[string]string{}, "POST", *apiEndpoint, nil)

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error powering on vApp: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) PowerOff() (Task, error) {

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)
	apiEndpoint.Path += "/power/action/powerOff"

	req := vapp.client.NewRequest(map[string]string{}, "POST", *apiEndpoint, nil)

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error powering off vApp: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) Reboot() (Task, error) {

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)
	apiEndpoint.Path += "/power/action/reboot"

	req := vapp.client.NewRequest(map[string]string{}, "POST", *apiEndpoint, nil)

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error rebooting vApp: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) Reset() (Task, error) {

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)
	apiEndpoint.Path += "/power/action/reset"

	req := vapp.client.NewRequest(map[string]string{}, "POST", *apiEndpoint, nil)

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error resetting vApp: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) Suspend() (Task, error) {

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)
	apiEndpoint.Path += "/power/action/suspend"

	req := vapp.client.NewRequest(map[string]string{}, "POST", *apiEndpoint, nil)

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error suspending vApp: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) Shutdown() (Task, error) {

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)
	apiEndpoint.Path += "/power/action/shutdown"

	req := vapp.client.NewRequest(map[string]string{}, "POST", *apiEndpoint, nil)

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error shutting down vApp: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) Undeploy() (Task, error) {

	vu := &types.UndeployVAppParams{
		Xmlns:               "http://www.vmware.com/vcloud/v1.5",
		UndeployPowerAction: "powerOff",
	}

	output, err := xml.MarshalIndent(vu, "  ", "    ")
	if err != nil {
		fmt.Printf("error: %v\n", err)
	}

	util.Logger.Printf("\n\nXML DEBUG: %s\n\n", string(output))

	buffer := bytes.NewBufferString(xml.Header + string(output))

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)
	apiEndpoint.Path += "/action/undeploy"

	req := vapp.client.NewRequest(map[string]string{}, "POST", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.undeployVAppParams+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error undeploy vApp: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) Deploy() (Task, error) {

	vu := &types.DeployVAppParams{
		Xmlns:   "http://www.vmware.com/vcloud/v1.5",
		PowerOn: false,
	}

	output, err := xml.MarshalIndent(vu, "  ", "    ")
	if err != nil {
		fmt.Printf("error: %v\n", err)
	}

	util.Logger.Printf("\n\nXML DEBUG: %s\n\n", string(output))

	buffer := bytes.NewBufferString(xml.Header + string(output))

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)
	apiEndpoint.Path += "/action/deploy"

	req := vapp.client.NewRequest(map[string]string{}, "POST", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.deployVAppParams+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error undeploy vApp: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) Delete() (Task, error) {

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)

	req := vapp.client.NewRequest(map[string]string{}, "DELETE", *apiEndpoint, nil)

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error deleting vApp: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) RunCustomizationScript(computername, script string) (Task, error) {
	return vapp.Customize(computername, script, false)
}

func (vapp *VApp) Customize(computername, script string, changeSid bool) (Task, error) {
	err := vapp.Refresh()
	if err != nil {
		return Task{}, fmt.Errorf("error refreshing vApp before running customization: %v", err)
	}

	// Check if VApp Children is populated
	if vapp.VApp.Children == nil {
		return Task{}, fmt.Errorf("vApp doesn't contain any children, aborting customization")
	}

	vu := &types.GuestCustomizationSection{
		Ovf:   "http://schemas.dmtf.org/ovf/envelope/1",
		Xsi:   "http://www.w3.org/2001/XMLSchema-instance",
		Xmlns: "http://www.vmware.com/vcloud/v1.5",

		HREF:                vapp.VApp.Children.VM[0].HREF,
		Type:                "application/vnd.vmware.vcloud.guestCustomizationSection+xml",
		Info:                "Specifies Guest OS Customization Settings",
		Enabled:             true,
		ComputerName:        computername,
		CustomizationScript: script,
		ChangeSid:           false,
	}

	output, err := xml.MarshalIndent(vu, "  ", "    ")
	if err != nil {
		fmt.Printf("error: %v\n", err)
	}

	util.Logger.Printf("[DEBUG] VCD Client configuration: %s", output)

	util.Logger.Printf("\n\nXML DEBUG: %s\n\n", string(output))

	buffer := bytes.NewBufferString(xml.Header + string(output))

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.Children.VM[0].HREF)
	apiEndpoint.Path += "/guestCustomizationSection/"

	req := vapp.client.NewRequest(map[string]string{}, "PUT", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.guestCustomizationSection+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error customizing VM: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil
}

func (vapp *VApp) GetStatus() (string, error) {
	err := vapp.Refresh()
	if err != nil {
		return "", fmt.Errorf("error refreshing vApp: %v", err)
	}
	return types.VAppStatuses[vapp.VApp.Status], nil
}

// BlockWhileStatus blocks until the status of vApp exits unwantedStatus.
// It sleeps 200 milliseconds between iterations and times out after timeOutAfterSeconds
// of seconds.
func (vapp *VApp) BlockWhileStatus(unwantedStatus string, timeOutAfterSeconds int) error {
	timeoutAfter := time.After(time.Duration(timeOutAfterSeconds) * time.Second)
	tick := time.Tick(200 * time.Millisecond)

	for {
		select {
		case <-timeoutAfter:
			return fmt.Errorf("timed out waiting for vApp to exit state %s after %d seconds",
				unwantedStatus, timeOutAfterSeconds)
		case <-tick:
			currentStatus, err := vapp.GetStatus()

			if err != nil {
				return fmt.Errorf("could not get vApp status %s", err)
			}
			if currentStatus != unwantedStatus {
				return nil
			}
		}
	}
}

func (vapp *VApp) GetNetworkConnectionSection() (*types.NetworkConnectionSection, error) {

	networkConnectionSection := &types.NetworkConnectionSection{}

	if vapp.VApp.Children.VM[0].HREF == "" {
		return networkConnectionSection, fmt.Errorf("cannot refresh, Object is empty")
	}

	getNetworkUrl, _ := url.ParseRequestURI(vapp.VApp.Children.VM[0].HREF + "/networkConnectionSection/")

	req := vapp.client.NewRequest(map[string]string{}, "GET", *getNetworkUrl, nil)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.networkConnectionSection+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return networkConnectionSection, fmt.Errorf("error retrieving task: %s", err)
	}

	if err = decodeBody(resp, networkConnectionSection); err != nil {
		return networkConnectionSection, fmt.Errorf("error decoding task response: %s", err)
	}

	// The request was successful
	return networkConnectionSection, nil
}

// Sets number of available virtual logical processors
// (i.e. CPUs x cores per socket)
// https://communities.vmware.com/thread/576209
// Deprecated: Use vm.ChangeCPUcount()
func (vapp *VApp) ChangeCPUCount(virtualCpuCount int) (Task, error) {
	return vapp.ChangeCPUCountWithCore(virtualCpuCount, nil)
}

// Sets number of available virtual logical processors
// (i.e. CPUs x cores per socket) and cores per socket.
// Socket count is a result of: virtual logical processors/cores per socket
// https://communities.vmware.com/thread/576209
// Deprecated: Use vm.ChangeCPUCountWithCore()
func (vapp *VApp) ChangeCPUCountWithCore(virtualCpuCount int, coresPerSocket *int) (Task, error) {

	err := vapp.Refresh()
	if err != nil {
		return Task{}, fmt.Errorf("error refreshing vApp before running customization: %v", err)
	}

	// Check if VApp Children is populated
	if vapp.VApp.Children == nil {
		return Task{}, fmt.Errorf("vApp doesn't contain any children, aborting customization")
	}

	newcpu := &types.OVFItem{
		XmlnsRasd:       "http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_ResourceAllocationSettingData",
		XmlnsVCloud:     "http://www.vmware.com/vcloud/v1.5",
		XmlnsXsi:        "http://www.w3.org/2001/XMLSchema-instance",
		XmlnsVmw:        "http://www.vmware.com/schema/ovf",
		VCloudHREF:      vapp.VApp.Children.VM[0].HREF + "/virtualHardwareSection/cpu",
		VCloudType:      "application/vnd.vmware.vcloud.rasdItem+xml",
		AllocationUnits: "hertz * 10^6",
		Description:     "Number of Virtual CPUs",
		ElementName:     strconv.Itoa(virtualCpuCount) + " virtual CPU(s)",
		InstanceID:      4,
		Reservation:     0,
		ResourceType:    3,
		VirtualQuantity: virtualCpuCount,
		Weight:          0,
		CoresPerSocket:  coresPerSocket,
		Link: &types.Link{
			HREF: vapp.VApp.Children.VM[0].HREF + "/virtualHardwareSection/cpu",
			Rel:  "edit",
			Type: "application/vnd.vmware.vcloud.rasdItem+xml",
		},
	}

	output, err := xml.MarshalIndent(newcpu, "  ", "    ")
	if err != nil {
		fmt.Printf("error: %v\n", err)
	}

	buffer := bytes.NewBufferString(xml.Header + string(output))

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.Children.VM[0].HREF)
	apiEndpoint.Path += "/virtualHardwareSection/cpu"

	req := vapp.client.NewRequest(map[string]string{}, "PUT", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.rasdItem+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error customizing VM: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) ChangeStorageProfile(name string) (Task, error) {
	err := vapp.Refresh()
	if err != nil {
		return Task{}, fmt.Errorf("error refreshing vApp before running customization: %v", err)
	}

	if vapp.VApp.Children == nil || len(vapp.VApp.Children.VM) == 0 {
		return Task{}, fmt.Errorf("vApp doesn't contain any children, aborting customization")
	}

	vdc, err := vapp.getParentVDC()
	if err != nil {
		return Task{}, fmt.Errorf("error retrieving parent VDC for vApp %s", vapp.VApp.Name)
	}
	storageProfileRef, err := vdc.FindStorageProfileReference(name)
	if err != nil {
		return Task{}, fmt.Errorf("error retrieving storage profile %s for vApp %s", name, vapp.VApp.Name)
	}

	newProfile := &types.VM{
		Name:           vapp.VApp.Children.VM[0].Name,
		StorageProfile: &storageProfileRef,
		Xmlns:          "http://www.vmware.com/vcloud/v1.5",
	}

	output, err := xml.MarshalIndent(newProfile, "  ", "    ")
	if err != nil {
		return Task{}, fmt.Errorf("error encoding storage profile change metadata for vApp %s", vapp.VApp.Name)
	}

	util.Logger.Printf("[DEBUG] VCD Client configuration: %s", output)

	buffer := bytes.NewBufferString(xml.Header + string(output))

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.Children.VM[0].HREF)

	req := vapp.client.NewRequest(map[string]string{}, "PUT", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.vm+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error customizing VM: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) ChangeVMName(name string) (Task, error) {
	err := vapp.Refresh()
	if err != nil {
		return Task{}, fmt.Errorf("error refreshing vApp before running customization: %v", err)
	}

	if vapp.VApp.Children == nil {
		return Task{}, fmt.Errorf("vApp doesn't contain any children, aborting customization")
	}

	newname := &types.VM{
		Name:  name,
		Xmlns: "http://www.vmware.com/vcloud/v1.5",
	}

	output, err := xml.MarshalIndent(newname, "  ", "    ")
	if err != nil {
		fmt.Printf("error: %v\n", err)
	}

	util.Logger.Printf("[DEBUG] VCD Client configuration: %s", output)

	buffer := bytes.NewBufferString(xml.Header + string(output))

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.Children.VM[0].HREF)

	req := vapp.client.NewRequest(map[string]string{}, "PUT", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.vm+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error customizing VM: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

// GetMetadata() function calls private function getMetadata() with vapp.client and vapp.VApp.HREF
// which returns a *types.Metadata struct for provided vapp input.
func (vapp *VApp) GetMetadata() (*types.Metadata, error) {
	return getMetadata(vapp.client, vapp.VApp.HREF)
}

func getMetadata(client *Client, requestUri string) (*types.Metadata, error) {
	metadata := &types.Metadata{}

	getMetadata, _ := url.ParseRequestURI(requestUri + "/metadata/")

	req := client.NewRequest(map[string]string{}, "GET", *getMetadata, nil)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.metadata+xml")

	resp, err := checkResp(client.Http.Do(req))
	if err != nil {
		return metadata, fmt.Errorf("error retrieving task: %s", err)
	}

	if err = decodeBody(resp, metadata); err != nil {
		return metadata, fmt.Errorf("error decoding task response: %s", err)
	}

	// The request was successful
	return metadata, nil
}

// DeleteMetadata() function calls private function deleteMetadata() with vapp.client and vapp.VApp.HREF
// which deletes metadata depending on key provided as input from vApp.
func (vapp *VApp) DeleteMetadata(key string) (Task, error) {
	return deleteMetadata(vapp.client, key, vapp.VApp.HREF)
}

// Deletes metadata (type MetadataStringValue) from the vApp
// TODO: Support all MetadataTypedValue types with this function
func deleteMetadata(client *Client, key string, requestUri string) (Task, error) {
	apiEndpoint, _ := url.ParseRequestURI(requestUri)
	apiEndpoint.Path += "/metadata/" + key

	req := client.NewRequest(map[string]string{}, "DELETE", *apiEndpoint, nil)

	resp, err := checkResp(client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error deleting metadata: %s", err)
	}

	task := NewTask(client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil
}

// AddMetadata() function calls private function addMetadata() with vapp.client and vapp.VApp.HREF
// which adds metadata key, value pair provided as input.
func (vapp *VApp) AddMetadata(key string, value string) (Task, error) {
	return addMetadata(vapp.client, key, value, vapp.VApp.HREF)
}

// Adds metadata (type MetadataStringValue) to the vApp
// TODO: Support all MetadataTypedValue types with this function
func addMetadata(client *Client, key string, value string, requestUri string) (Task, error) {
	newmetadata := &types.MetadataValue{
		Xmlns: "http://www.vmware.com/vcloud/v1.5",
		Xsi:   "http://www.w3.org/2001/XMLSchema-instance",
		TypedValue: &types.TypedValue{
			XsiType: "MetadataStringValue",
			Value:   value,
		},
	}

	output, err := xml.MarshalIndent(newmetadata, "  ", "    ")
	if err != nil {
		return Task{}, fmt.Errorf("error adding metadata: %s", err)
	}

	buffer := bytes.NewBufferString(xml.Header + string(output))

	apiEndpoint, _ := url.ParseRequestURI(requestUri)
	apiEndpoint.Path += "/metadata/" + key

	req := client.NewRequest(map[string]string{}, "PUT", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.metadata.value+xml")

	resp, err := checkResp(client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error customizing vApp metadata: %s", err)
	}

	task := NewTask(client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil
}

func (vapp *VApp) SetOvf(parameters map[string]string) (Task, error) {
	err := vapp.Refresh()
	if err != nil {
		return Task{}, fmt.Errorf("error refreshing vApp before running customization: %v", err)
	}

	if vapp.VApp.Children == nil {
		return Task{}, fmt.Errorf("vApp doesn't contain any children, aborting customization")
	}

	if vapp.VApp.Children.VM[0].ProductSection == nil {
		return Task{}, fmt.Errorf("vApp doesn't contain any children with ProductSection, aborting customization")
	}

	for key, value := range parameters {
		for _, ovf_value := range vapp.VApp.Children.VM[0].ProductSection.Property {
			if ovf_value.Key == key {
				ovf_value.Value = &types.Value{Value: value}
				break
			}
		}
	}

	newmetadata := &types.ProductSectionList{
		Xmlns:          "http://www.vmware.com/vcloud/v1.5",
		Ovf:            "http://schemas.dmtf.org/ovf/envelope/1",
		ProductSection: vapp.VApp.Children.VM[0].ProductSection,
	}

	output, err := xml.MarshalIndent(newmetadata, "  ", "    ")
	if err != nil {
		fmt.Printf("error: %v\n", err)
	}

	util.Logger.Printf("[DEBUG] NetworkXML: %s", output)

	buffer := bytes.NewBufferString(xml.Header + string(output))

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.Children.VM[0].HREF)
	apiEndpoint.Path += "/productSections"

	req := vapp.client.NewRequest(map[string]string{}, "PUT", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.productSections+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error customizing VM Network: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) ChangeMemorySize(size int) (Task, error) {

	err := vapp.Refresh()
	if err != nil {
		return Task{}, fmt.Errorf("error refreshing vApp before running customization: %v", err)
	}

	// Check if VApp Children is populated
	if vapp.VApp.Children == nil {
		return Task{}, fmt.Errorf("vApp doesn't contain any children, aborting customization")
	}

	newmem := &types.OVFItem{
		XmlnsRasd:       "http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_ResourceAllocationSettingData",
		XmlnsVCloud:     "http://www.vmware.com/vcloud/v1.5",
		XmlnsXsi:        "http://www.w3.org/2001/XMLSchema-instance",
		VCloudHREF:      vapp.VApp.Children.VM[0].HREF + "/virtualHardwareSection/memory",
		VCloudType:      "application/vnd.vmware.vcloud.rasdItem+xml",
		AllocationUnits: "byte * 2^20",
		Description:     "Memory Size",
		ElementName:     strconv.Itoa(size) + " MB of memory",
		InstanceID:      5,
		Reservation:     0,
		ResourceType:    4,
		VirtualQuantity: size,
		Weight:          0,
		Link: &types.Link{
			HREF: vapp.VApp.Children.VM[0].HREF + "/virtualHardwareSection/memory",
			Rel:  "edit",
			Type: "application/vnd.vmware.vcloud.rasdItem+xml",
		},
	}

	output, err := xml.MarshalIndent(newmem, "  ", "    ")
	if err != nil {
		return Task{}, fmt.Errorf("error: %v\n", err)
	}

	util.Logger.Printf("\n\nXML DEBUG: %s\n\n", string(output))

	buffer := bytes.NewBufferString(xml.Header + string(output))

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.Children.VM[0].HREF)
	apiEndpoint.Path += "/virtualHardwareSection/memory"

	req := vapp.client.NewRequest(map[string]string{}, "PUT", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.rasdItem+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error customizing VM: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) GetNetworkConfigSection() (*types.NetworkConfigSection, error) {

	networkConfig := &types.NetworkConfigSection{}

	if vapp.VApp.HREF == "" {
		return networkConfig, fmt.Errorf("cannot refresh, Object is empty")
	}

	getNetworkUrl, _ := url.ParseRequestURI(vapp.VApp.HREF + "/networkConfigSection/")

	req := vapp.client.NewRequest(map[string]string{}, "GET", *getNetworkUrl, nil)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.networkConfigSection+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return networkConfig, fmt.Errorf("error retrieving task: %s", err)
	}

	if err = decodeBody(resp, networkConfig); err != nil {
		return networkConfig, fmt.Errorf("error decoding task response: %s", err)
	}

	// The request was successful
	return networkConfig, nil
}

// Function adds existing VDC network to vApp
func (vapp *VApp) AddRAWNetworkConfig() (Task, error) {

	vAppNetworkConfig, err := vapp.GetNetworkConfigSection()
	if err != nil {
		return Task{}, fmt.Errorf("error getting vApp networks: %#v", err)
	}
	networkConfigurations := vAppNetworkConfig.NetworkConfig

	output, err := xml.MarshalIndent(networkConfigurations, "  ", "    ")
	if err != nil {
		fmt.Printf("error: %v\n", err)
	}

	util.Logger.Printf("[DEBUG] RAWNETWORK Config NetworkXML: %s", output)

	buffer := bytes.NewBufferString(xml.Header + string(output))

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)
	apiEndpoint.Path += "/networkConfigSection/"

	req := vapp.client.NewRequest(map[string]string{}, "PUT", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.networkconfigsection+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error adding vApp Network: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

func (vapp *VApp) AppendNetworkConfig(orgvdcnetworks *types.OrgVDCNetwork) (Task, error) {

	networkConfigSection, err := vapp.GetNetworkConfigSection()
	if err != nil {
		fmt.Printf("error: %v\n", err)
	}

	networkConfigSection.Info = "Configuration parameters for logical networks"
	networkConfigSection.Ovf = "http://schemas.dmtf.org/ovf/envelope/1"
	networkConfigSection.Type = "application/vnd.vmware.vcloud.networkConfigSection+xml"
	networkConfigSection.Xmlns = "http://www.vmware.com/vcloud/v1.5"

	networkConfigSection.NetworkConfig = append(networkConfigSection.NetworkConfig,
		types.VAppNetworkConfiguration{
			NetworkName: orgvdcnetworks.Name,
			Configuration: &types.NetworkConfiguration{
				ParentNetwork: &types.Reference{
					HREF: orgvdcnetworks.HREF,
				},
				FenceMode: "bridged",
			},
		},
	)

	output, err := xml.MarshalIndent(networkConfigSection, "  ", "    ")
	if err != nil {
		fmt.Printf("error: %v\n", err)
	}

	util.Logger.Printf("[DEBUG] AppendNetworkConfig Config NetworkXML: %s", output)

	buffer := bytes.NewBufferString(xml.Header + string(output))

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)
	apiEndpoint.Path += "/networkConfigSection/"

	req := vapp.client.NewRequest(map[string]string{}, "PUT", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.networkconfigsection+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error adding vApp Network: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil

}

// Function allows to create isolated network for vApp. This is equivalent to vCD UI function - vApp network creation.
func (vapp *VApp) AddIsolatedNetwork(newIsolatedNetworkSettings *VappNetworkSettings) (Task, error) {

	err := validateNetworkConfigSettings(newIsolatedNetworkSettings)
	if err != nil {
		return Task{}, err
	}

	// for case when range is one ip address
	if newIsolatedNetworkSettings.DhcpSettings != nil && newIsolatedNetworkSettings.DhcpSettings.IPRange != nil && newIsolatedNetworkSettings.DhcpSettings.IPRange.EndAddress == "" {
		newIsolatedNetworkSettings.DhcpSettings.IPRange.EndAddress = newIsolatedNetworkSettings.DhcpSettings.IPRange.StartAddress
	}

	// explicitly check if to add data, to not send any values
	var networkFeatures *types.NetworkFeatures
	if newIsolatedNetworkSettings.DhcpSettings != nil {
		networkFeatures = &types.NetworkFeatures{DhcpService: &types.DhcpService{
			IsEnabled:        newIsolatedNetworkSettings.DhcpSettings.IsEnabled,
			DefaultLeaseTime: newIsolatedNetworkSettings.DhcpSettings.DefaultLeaseTime,
			MaxLeaseTime:     newIsolatedNetworkSettings.DhcpSettings.MaxLeaseTime,
			IPRange:          newIsolatedNetworkSettings.DhcpSettings.IPRange}}
	}

	networkConfigurations := vapp.VApp.NetworkConfigSection.NetworkConfig
	networkConfigurations = append(networkConfigurations,
		types.VAppNetworkConfiguration{
			NetworkName: newIsolatedNetworkSettings.Name,
			Configuration: &types.NetworkConfiguration{
				FenceMode:        "isolated",
				GuestVlanAllowed: newIsolatedNetworkSettings.GuestVLANAllowed,
				Features:         networkFeatures,
				IPScopes: &types.IPScopes{IPScope: types.IPScope{IsInherited: false, Gateway: newIsolatedNetworkSettings.Gateway,
					Netmask: newIsolatedNetworkSettings.NetMask, DNS1: newIsolatedNetworkSettings.DNS1,
					DNS2: newIsolatedNetworkSettings.DNS2, DNSSuffix: newIsolatedNetworkSettings.DNSSuffix, IsEnabled: true,
					IPRanges: &types.IPRanges{IPRange: newIsolatedNetworkSettings.StaticIPRanges}}},
			},
			IsDeployed: false,
		})

	return updateNetworkConfigurations(vapp, networkConfigurations)

}

func validateNetworkConfigSettings(networkSettings *VappNetworkSettings) error {
	if networkSettings.Name == "" {
		return errors.New("network name is missing")
	}

	if networkSettings.Gateway == "" {
		return errors.New("network gateway IP is missing")
	}

	if networkSettings.NetMask == "" {
		return errors.New("network mask config is missing")
	}

	if networkSettings.NetMask == "" {
		return errors.New("network mask config is missing")
	}

	if networkSettings.DhcpSettings != nil && networkSettings.DhcpSettings.IPRange == nil {
		return errors.New("network DHCP ip range config is missing")
	}

	if networkSettings.DhcpSettings != nil && networkSettings.DhcpSettings.IPRange.StartAddress == "" {
		return errors.New("network DHCP ip range start address is missing")
	}

	return nil
}

// Removes vApp isolated network
func (vapp *VApp) RemoveIsolatedNetwork(networkName string) (Task, error) {

	if networkName == "" {
		return Task{}, fmt.Errorf("network name can't be empty")
	}

	networkConfigurations := vapp.VApp.NetworkConfigSection.NetworkConfig
	isNetworkFound := false
	for index, networkConfig := range networkConfigurations {
		if networkConfig.NetworkName == networkName {
			isNetworkFound = true
			networkConfigurations = append(networkConfigurations[:index], networkConfigurations[index+1:]...)
		}
	}

	if !isNetworkFound {
		return Task{}, fmt.Errorf("network to remove %s, wasn't found", networkName)
	}

	return updateNetworkConfigurations(vapp, networkConfigurations)
}

// Function allows to update vApp network configuration. This works for updating, deleting and adding.
// Network configuration has to be full with new, changed elements and unchanged.
// https://opengrok.eng.vmware.com/source/xref/cloud-sp-main.perforce-shark.1700/sp-main/dev-integration/system-tests/SystemTests/src/main/java/com/vmware/cloud/systemtests/util/VAppNetworkUtils.java#createVAppNetwork
// http://pubs.vmware.com/vcloud-api-1-5/wwhelp/wwhimpl/js/html/wwhelp.htm#href=api_prog/GUID-92622A15-E588-4FA1-92DA-A22A4757F2A0.html#1_14_12_10_1
func updateNetworkConfigurations(vapp *VApp, networkConfigurations []types.VAppNetworkConfiguration) (Task, error) {
	networkConfig := &types.NetworkConfigSection{
		Info:          "Configuration parameters for logical networks",
		Ovf:           "http://schemas.dmtf.org/ovf/envelope/1",
		Type:          "application/vnd.vmware.vcloud.networkConfigSection+xml",
		Xmlns:         "http://www.vmware.com/vcloud/v1.5",
		NetworkConfig: networkConfigurations,
	}

	output, err := xml.MarshalIndent(networkConfig, "  ", "    ")
	if err != nil {
		fmt.Printf("error: %v\n", err)
	}

	util.Logger.Printf("[DEBUG] AppendNetworkConfig Config NetworkXML: %s", output)

	buffer := bytes.NewBufferString(xml.Header + string(output))

	apiEndpoint, _ := url.ParseRequestURI(vapp.VApp.HREF)
	apiEndpoint.Path += "/networkConfigSection/"

	req := vapp.client.NewRequest(map[string]string{}, "PUT", *apiEndpoint, buffer)

	req.Header.Add("Content-Type", "application/vnd.vmware.vcloud.networkconfigsection+xml")

	resp, err := checkResp(vapp.client.Http.Do(req))
	if err != nil {
		return Task{}, fmt.Errorf("error updating vApp Network: %s", err)
	}

	task := NewTask(vapp.client)

	if err = decodeBody(resp, task.Task); err != nil {
		return Task{}, fmt.Errorf("error decoding Task response: %s", err)
	}

	// The request was successful
	return *task, nil
}
