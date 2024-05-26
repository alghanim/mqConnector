package models

import (
	"encoding/xml"
)

type Node struct {
	XMLName xml.Name
	Content []byte `xml:",innerxml"`
	Nodes   []Node `xml:",any"`
}

type ConfigEntry struct {
	FieldPath string `db:"FieldPath" json:"FieldPath"`
	Enabled   bool   `db:"Enabled" json:"Enabled"`
}

type MQConfig struct {
	Type         string `db:"type" json:"type"`
	QueueManager string `db:"queueManager" json:"queueManager"`
	ConnName     string `db:"connName" json:"connName"`
	Channel      string `cb:"channel" json:"channel"`
	User         string `db:"user" json:"user"`
	Password     string `db:"password" json:"password"`
	QueueName    string `db:"queueName" json:"queueName"`
	URL          string `db:"url" json:"url"`
	Brokers      string `db:"brokers" json:"brokers"`
	Topic        string `db:"topic" json:"topic"`
	OwnerName    string `db:"ownerName" json:"ownerName"`
}
