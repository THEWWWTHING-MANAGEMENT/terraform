package aws

import (
	"fmt"
	"log"
	"strings"

	"github.com/hashicorp/terraform/helper/schema"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/sns"
	"time"
)

const awsSNSPendingConfirmationMessage = "pending confirmation"

func resourceAwsSnsTopicSubscription() *schema.Resource {
	return &schema.Resource{
		Create: resourceAwsSnsTopicSubscriptionCreate,
		Read:   resourceAwsSnsTopicSubscriptionRead,
		Update: resourceAwsSnsTopicSubscriptionUpdate,
		Delete: resourceAwsSnsTopicSubscriptionDelete,

		Schema: map[string]*schema.Schema{
			"protocol": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
				ForceNew: false,
				ValidateFunc: func(v interface{}, k string) (ws []string, errors []error) {
					value := v.(string)
					forbidden := []string{"email", "sms"}
					for _, f := range forbidden {
						if strings.Contains(value, f) {
							errors = append(
								errors,
								fmt.Errorf("Unsupported protocol (%s) for SNS Topic", value),
							)
						}
					}
					return
				},
			},
			"endpoint": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
				ForceNew: false,
			},
			"endpoint_auto_confirms": &schema.Schema{
				Type:     schema.TypeBool,
				Optional: true,
				ForceNew: false,
				Default:  false,
			},
			"max_fetch_retries": &schema.Schema{
				Type:     schema.TypeInt,
				Optional: true,
				ForceNew: false,
				Default:  3,
			},
			"fetch_retry_delay": &schema.Schema{
				Type:     schema.TypeInt,
				Optional: true,
				ForceNew: false,
				Default:  1,
			},
			"topic_arn": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
				ForceNew: false,
			},
			"delivery_policy": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: false,
			},
			"raw_message_delivery": &schema.Schema{
				Type:     schema.TypeBool,
				Optional: true,
				ForceNew: false,
				Default:  false,
			},
			"arn": &schema.Schema{
				Type:     schema.TypeString,
				Computed: true,
			},
		},
	}
}

func resourceAwsSnsTopicSubscriptionCreate(d *schema.ResourceData, meta interface{}) error {
	snsconn := meta.(*AWSClient).snsconn

	output, err := subscribeToSNSTopic(d, snsconn)

	if err != nil {
		return err
	}

	if output.SubscriptionArn != nil && *output.SubscriptionArn == awsSNSPendingConfirmationMessage {
		log.Printf("[WARN] Invalid SNS Subscription, received a \"%s\" ARN", awsSNSPendingConfirmationMessage)
		return nil
	}

	log.Printf("New subscription ARN: %s", *output.SubscriptionArn)
	d.SetId(*output.SubscriptionArn)

	// Write the ARN to the 'arn' field for export
	d.Set("arn", *output.SubscriptionArn)

	return resourceAwsSnsTopicSubscriptionUpdate(d, meta)
}

func resourceAwsSnsTopicSubscriptionUpdate(d *schema.ResourceData, meta interface{}) error {
	snsconn := meta.(*AWSClient).snsconn

	// If any changes happened, un-subscribe and re-subscribe
	if d.HasChange("protocol") || d.HasChange("endpoint") || d.HasChange("topic_arn") {
		log.Printf("[DEBUG] Updating subscription %s", d.Id())
		// Unsubscribe
		_, err := snsconn.Unsubscribe(&sns.UnsubscribeInput{
			SubscriptionArn: aws.String(d.Id()),
		})

		if err != nil {
			return fmt.Errorf("Error unsubscribing from SNS topic: %s", err)
		}

		// Re-subscribe and set id
		output, err := subscribeToSNSTopic(d, snsconn)
		d.SetId(*output.SubscriptionArn)
		d.Set("arn", *output.SubscriptionArn)
	}

	if d.HasChange("raw_message_delivery") {
		_, n := d.GetChange("raw_message_delivery")

		attrValue := "false"

		if n.(bool) {
			attrValue = "true"
		}

		req := &sns.SetSubscriptionAttributesInput{
			SubscriptionArn: aws.String(d.Id()),
			AttributeName:   aws.String("RawMessageDelivery"),
			AttributeValue:  aws.String(attrValue),
		}
		_, err := snsconn.SetSubscriptionAttributes(req)

		if err != nil {
			return fmt.Errorf("Unable to set raw message delivery attribute on subscription")
		}
	}

	return resourceAwsSnsTopicSubscriptionRead(d, meta)
}

func resourceAwsSnsTopicSubscriptionRead(d *schema.ResourceData, meta interface{}) error {
	snsconn := meta.(*AWSClient).snsconn

	log.Printf("[DEBUG] Loading subscription %s", d.Id())

	attributeOutput, err := snsconn.GetSubscriptionAttributes(&sns.GetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(d.Id()),
	})
	if err != nil {
		return err
	}

	if attributeOutput.Attributes != nil && len(attributeOutput.Attributes) > 0 {
		attrHash := attributeOutput.Attributes
		log.Printf("[DEBUG] raw message delivery: %s", *attrHash["RawMessageDelivery"])
		if *attrHash["RawMessageDelivery"] == "true" {
			d.Set("raw_message_delivery", true)
		} else {
			d.Set("raw_message_delivery", false)
		}
	}

	return nil
}

func resourceAwsSnsTopicSubscriptionDelete(d *schema.ResourceData, meta interface{}) error {
	snsconn := meta.(*AWSClient).snsconn

	log.Printf("[DEBUG] SNS delete topic subscription: %s", d.Id())
	_, err := snsconn.Unsubscribe(&sns.UnsubscribeInput{
		SubscriptionArn: aws.String(d.Id()),
	})
	if err != nil {
		return err
	}
	return nil
}

func subscribeToSNSTopic(d *schema.ResourceData, snsconn *sns.SNS) (output *sns.SubscribeOutput, err error) {
	protocol := d.Get("protocol").(string)
	endpoint := d.Get("endpoint").(string)
	topic_arn := d.Get("topic_arn").(string)
	endpoint_auto_confirms := d.Get("endpoint_auto_confirms").(bool)
	max_fetch_retries := d.Get("max_fetch_retries").(int)
	fetch_retry_delay := time.Duration(d.Get("fetch_retry_delay").(int))

	if strings.Contains(protocol, "http") && !endpoint_auto_confirms {
		return nil, fmt.Errorf("Protocol http/https is only supported for endpoints which auto confirms!")
	}

	log.Printf("[DEBUG] SNS create topic subscription: %s (%s) @ '%s'", endpoint, protocol, topic_arn)

	req := &sns.SubscribeInput{
		Protocol: aws.String(protocol),
		Endpoint: aws.String(endpoint),
		TopicArn: aws.String(topic_arn),
	}

	output, err = snsconn.Subscribe(req)
	if err != nil {
		return nil, fmt.Errorf("Error creating SNS topic: %s", err)
	}

	if strings.Contains(protocol, "http") && (output.SubscriptionArn == nil || *output.SubscriptionArn == awsSNSPendingConfirmationMessage) {

		log.Printf("[DEBUG] SNS create topic subscritpion is pending so fetching the subscription list for topic : %s (%s) @ '%s'", endpoint, protocol, topic_arn)

		for i := 0; i < max_fetch_retries && output.SubscriptionArn != nil && *output.SubscriptionArn == awsSNSPendingConfirmationMessage; i++ {

			subscription, err := findSubscriptionByNonID(d, snsconn)

			if err != nil {
				return nil, fmt.Errorf("Error fetching subscriptions for SNS topic %s: %s", topic_arn, err)
			}

			if subscription != nil {
				output.SubscriptionArn = subscription.SubscriptionArn
				break
			}

			time.Sleep(time.Second * fetch_retry_delay)
		}

		if output.SubscriptionArn == nil || *output.SubscriptionArn == awsSNSPendingConfirmationMessage {
			return nil, fmt.Errorf("Endpoint (%s) did not autoconfirm the subscription for topic %s", endpoint, topic_arn)
		}
	}

	log.Printf("[DEBUG] Created new subscription!")
	return output, nil
}

// finds a subscription using protocol, endpoint and topic_arn (which is a key in sns subscription)
func findSubscriptionByNonID(d *schema.ResourceData, snsconn *sns.SNS) (*sns.Subscription, error) {
	protocol := d.Get("protocol").(string)
	endpoint := d.Get("endpoint").(string)
	topic_arn := d.Get("topic_arn").(string)

	req := &sns.ListSubscriptionsByTopicInput{
		TopicArn: aws.String(topic_arn),
	}

	for {

		res, err := snsconn.ListSubscriptionsByTopic(req)

		if err != nil {
			return nil, fmt.Errorf("Error fetching subscripitions for topic %s : %s", topic_arn, err)
		}

		for _, subscription := range res.Subscriptions {
			if *subscription.Endpoint == endpoint && *subscription.Protocol == protocol && *subscription.TopicArn == topic_arn && *subscription.SubscriptionArn != awsSNSPendingConfirmationMessage {
				return subscription, nil
			}
		}

		// if there are more than 100 subscriptions then go to the next 100 otherwise return nil
		if res.NextToken != nil {
			req.NextToken = res.NextToken
		} else {
			return nil, nil
		}
	}
}
