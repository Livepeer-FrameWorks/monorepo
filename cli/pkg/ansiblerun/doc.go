// Package ansiblerun drives ansible-playbook via apenella/go-ansible v2.
//
// Owns: invocation, inventory rendering, collection + role cache install,
// and output streaming. Consumed by cli/pkg/provisioner/role_provisioner.go
// for every role-backed service.
package ansiblerun
