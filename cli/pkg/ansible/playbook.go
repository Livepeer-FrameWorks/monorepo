package ansible

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// NewPlaybook creates a new Ansible playbook
func NewPlaybook(name, hosts string) *Playbook {
	return &Playbook{
		Name:  name,
		Hosts: hosts,
		Plays: []Play{},
	}
}

// AddPlay adds a play to the playbook
func (p *Playbook) AddPlay(play Play) {
	p.Plays = append(p.Plays, play)
}

// ToYAML converts the playbook to YAML format
func (p *Playbook) ToYAML() ([]byte, error) {
	// Convert to Ansible playbook structure
	plays := make([]map[string]interface{}, 0, len(p.Plays))

	for _, play := range p.Plays {
		playMap := map[string]interface{}{
			"name":  play.Name,
			"hosts": play.Hosts,
		}

		if play.BecomeUser != "" {
			playMap["become"] = play.Become
			playMap["become_user"] = play.BecomeUser
		} else if play.Become {
			playMap["become"] = true
		}

		if play.GatherFacts {
			playMap["gather_facts"] = true
		} else {
			playMap["gather_facts"] = false
		}

		if len(play.Vars) > 0 {
			playMap["vars"] = play.Vars
		}

		if len(play.PreTasks) > 0 {
			playMap["pre_tasks"] = convertTasks(play.PreTasks)
		}

		if len(play.Roles) > 0 {
			playMap["roles"] = convertRoles(play.Roles)
		}

		if len(play.Tasks) > 0 {
			playMap["tasks"] = convertTasks(play.Tasks)
		}

		if len(play.PostTasks) > 0 {
			playMap["post_tasks"] = convertTasks(play.PostTasks)
		}

		if len(play.Handlers) > 0 {
			playMap["handlers"] = convertHandlers(play.Handlers)
		}

		plays = append(plays, playMap)
	}

	return yaml.Marshal(plays)
}

// convertTasks converts Task structs to Ansible task maps
func convertTasks(tasks []Task) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(tasks))

	for _, task := range tasks {
		taskMap := map[string]interface{}{
			"name": task.Name,
		}

		// Add module and args
		if task.Module != "" {
			taskMap[task.Module] = task.Args
		}

		if task.When != "" {
			taskMap["when"] = task.When
		}

		if task.Register != "" {
			taskMap["register"] = task.Register
		}

		if len(task.Notify) > 0 {
			taskMap["notify"] = task.Notify
		}

		if len(task.Tags) > 0 {
			taskMap["tags"] = task.Tags
		}

		if task.Ignore {
			taskMap["ignore_errors"] = true
		}

		result = append(result, taskMap)
	}

	return result
}

// convertRoles converts Role structs to Ansible role format
func convertRoles(roles []Role) []interface{} {
	result := make([]interface{}, 0, len(roles))

	for _, role := range roles {
		if len(role.Vars) > 0 {
			result = append(result, map[string]interface{}{
				"role": role.Name,
				"vars": role.Vars,
			})
		} else {
			result = append(result, role.Name)
		}
	}

	return result
}

// convertHandlers converts Handler structs to Ansible handler maps
func convertHandlers(handlers []Handler) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(handlers))

	for _, handler := range handlers {
		handlerMap := map[string]interface{}{
			"name":         handler.Name,
			handler.Module: handler.Args,
		}
		result = append(result, handlerMap)
	}

	return result
}

// GeneratePostgresPlaybook creates an Ansible playbook for PostgreSQL
func GeneratePostgresPlaybook(host, version string, databases []string) *Playbook {
	playbook := NewPlaybook("Provision PostgreSQL", host)

	play := Play{
		Name:        "Install and configure PostgreSQL",
		Hosts:       host,
		Become:      true,
		GatherFacts: true,
		Roles: []Role{
			{
				Name: "geerlingguy.postgresql",
				Vars: map[string]interface{}{
					"postgresql_version":   version,
					"postgresql_users":     []map[string]string{},
					"postgresql_databases": databases,
					"postgresql_global_config_options": []map[string]interface{}{
						{"option": "listen_addresses", "value": "*"},
						{"option": "max_connections", "value": 200},
					},
					"postgresql_hba_entries": []map[string]interface{}{
						{
							"type":     "local",
							"database": "all",
							"user":     "all",
							"method":   "peer",
						},
						{
							"type":     "host",
							"database": "all",
							"user":     "all",
							"address":  "127.0.0.1/32",
							"method":   "md5",
						},
						{
							"type":     "host",
							"database": "all",
							"user":     "all",
							"address":  "0.0.0.0/0",
							"method":   "md5",
						},
					},
				},
			},
		},
	}

	playbook.AddPlay(play)
	return playbook
}

// GenerateKafkaPlaybook creates an Ansible playbook for Kafka
func GenerateKafkaPlaybook(brokerID int, host string, zkConnect string) *Playbook {
	playbook := NewPlaybook("Provision Kafka", host)

	play := Play{
		Name:        "Install and configure Kafka",
		Hosts:       host,
		Become:      true,
		GatherFacts: true,
		Roles: []Role{
			{
				Name: "sleighzy.kafka",
				Vars: map[string]interface{}{
					"kafka_broker_id":         brokerID,
					"kafka_listener_hostname": host,
					"kafka_listener_port":     9092,
					"kafka_zookeeper_connect": zkConnect,
					"kafka_log_dirs":          "/var/lib/kafka/logs",
				},
			},
		},
	}

	playbook.AddPlay(play)
	return playbook
}

// String returns a string representation of the playbook
func (p *Playbook) String() string {
	yaml, err := p.ToYAML()
	if err != nil {
		return fmt.Sprintf("Error generating YAML: %v", err)
	}
	return string(yaml)
}

// Summary returns a brief summary of the playbook
func (p *Playbook) Summary() string {
	var parts []string
	parts = append(parts, fmt.Sprintf("Playbook: %s", p.Name))
	parts = append(parts, fmt.Sprintf("Hosts: %s", p.Hosts))
	parts = append(parts, fmt.Sprintf("Plays: %d", len(p.Plays)))
	return strings.Join(parts, ", ")
}
