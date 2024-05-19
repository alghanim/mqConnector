package mq

import (
	"fmt"

	"github.com/ibm-messaging/mq-golang/v5/ibmmq"
)

type IBMMQConnector struct {
	queueManager string
	connName     string
	channel      string
	user         string
	password     string
	queueName    string
	conn         *ibmmq.MQQueueManager
	queue        *ibmmq.MQObject
}

func (c *IBMMQConnector) Connect() error {
	// Create the connection options structure
	cno := ibmmq.NewMQCNO()
	cd := ibmmq.NewMQCD()
	cd.ChannelName = c.channel
	cd.ConnectionName = c.connName

	cno.ClientConn = cd

	if c.user != "" && c.password != "" {
		csp := ibmmq.NewMQCSP()
		csp.UserId = c.user
		csp.Password = c.password
		cno.SecurityParms = csp
	}

	// Connect to the queue manager
	qMgrObject, err := ibmmq.Connx(c.queueManager, cno)
	if err != nil {
		return fmt.Errorf("error connecting to queue manager: %v", err)
	}
	c.conn = &qMgrObject

	// Open the queue
	return c.OpenQueue()
}

func (c *IBMMQConnector) OpenQueue() error {
	// Specify the options for opening the queue
	od := ibmmq.NewMQOD()
	od.ObjectName = c.queueName
	od.ObjectType = ibmmq.MQOT_Q

	openOptions := ibmmq.MQOO_OUTPUT | ibmmq.MQOO_INPUT_AS_Q_DEF

	// Open the queue
	queueObject, err := c.conn.Open(od, openOptions)
	if err != nil {
		return fmt.Errorf("error opening queue: %v", err)
	}
	c.queue = &queueObject
	return nil
}

func (c *IBMMQConnector) Disconnect() error {
	if c.queue != nil {
		err := c.queue.Close(0)
		if err != nil {
			return fmt.Errorf("error closing queue: %v", err)
		}
	}
	if c.conn != nil {
		err := c.conn.Disc()
		if err != nil {
			return fmt.Errorf("error disconnecting from queue manager: %v", err)
		}
	}
	return nil
}

func (c *IBMMQConnector) SendMessage(message []byte) error {
	if c.queue == nil {
		return fmt.Errorf("queue not opened")
	}

	// Create a new message descriptor
	msgDesc := ibmmq.NewMQMD()
	pmo := ibmmq.NewMQPMO()

	// Set the message options
	pmo.Options = ibmmq.MQPMO_NO_SYNCPOINT

	// Create a new message
	err := c.queue.Put(msgDesc, pmo, message)
	if err != nil {
		return fmt.Errorf("error putting message: %v", err)
	}
	return nil
}

func (c *IBMMQConnector) ReceiveMessage() ([]byte, error) {
	if c.queue == nil {
		return nil, fmt.Errorf("queue not opened")
	}

	// Create a new message descriptor and get message options
	msgDesc := ibmmq.NewMQMD()
	gmo := ibmmq.NewMQGMO()

	// Set the get message options
	gmo.Options = ibmmq.MQGMO_WAIT
	gmo.WaitInterval = 3000 // Wait for 3 seconds

	buffer := make([]byte, 1024)

	// Get the message
	datalen, err := c.queue.Get(msgDesc, gmo, buffer)
	if err != nil {
		return nil, fmt.Errorf("error getting message: %v", err)
	}
	return buffer[:datalen], nil
}
