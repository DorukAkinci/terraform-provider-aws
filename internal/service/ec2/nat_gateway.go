package ec2

import (
	"fmt"
	"log"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/hashicorp/aws-sdk-go-base/tfawserr"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"github.com/hashicorp/terraform-provider-aws/internal/conns"
	tftags "github.com/hashicorp/terraform-provider-aws/internal/tags"
	"github.com/hashicorp/terraform-provider-aws/internal/verify"
)

func ResourceNatGateway() *schema.Resource {
	return &schema.Resource{
		Create: resourceNatGatewayCreate,
		Read:   resourceNatGatewayRead,
		Update: resourceNatGatewayUpdate,
		Delete: resourceNatGatewayDelete,

		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},

		Schema: map[string]*schema.Schema{
			"allocation_id": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
			},
			"connectivity_type": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				Default:      ec2.ConnectivityTypePublic,
				ValidateFunc: validation.StringInSlice(ec2.ConnectivityType_Values(), false),
			},
			"network_interface_id": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"private_ip": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"public_ip": {
				Type:     schema.TypeString,
				Computed: true,
			},
			"subnet_id": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
			"tags":     tftags.TagsSchema(),
			"tags_all": tftags.TagsSchemaComputed(),
		},

		CustomizeDiff: verify.SetTagsDiff,
	}
}

func resourceNatGatewayCreate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).EC2Conn
	defaultTagsConfig := meta.(*conns.AWSClient).DefaultTagsConfig
	tags := defaultTagsConfig.MergeTags(tftags.New(d.Get("tags").(map[string]interface{})))

	input := &ec2.CreateNatGatewayInput{
		TagSpecifications: ec2TagSpecificationsFromKeyValueTags(tags, ec2.ResourceTypeNatgateway),
	}

	if v, ok := d.GetOk("allocation_id"); ok {
		input.AllocationId = aws.String(v.(string))
	}

	if v, ok := d.GetOk("connectivity_type"); ok {
		input.ConnectivityType = aws.String(v.(string))
	}

	if v, ok := d.GetOk("subnet_id"); ok {
		input.SubnetId = aws.String(v.(string))
	}

	log.Printf("[DEBUG] Creating EC2 NAT Gateway: %s", input)
	output, err := conn.CreateNatGateway(input)

	if err != nil {
		return fmt.Errorf("error creating EC2 NAT Gateway: %w", err)
	}

	d.SetId(aws.StringValue(output.NatGateway.NatGatewayId))

	if _, err := WaitNATGatewayCreated(conn, d.Id()); err != nil {
		return fmt.Errorf("error waiting for EC2 NAT Gateway (%s) create: %w", d.Id(), err)
	}

	return resourceNatGatewayRead(d, meta)
}

func resourceNatGatewayRead(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).EC2Conn
	defaultTagsConfig := meta.(*conns.AWSClient).DefaultTagsConfig
	ignoreTagsConfig := meta.(*conns.AWSClient).IgnoreTagsConfig

	// Refresh the NAT Gateway state
	ngRaw, state, err := NGStateRefreshFunc(conn, d.Id())()
	if err != nil {
		return err
	}

	status := map[string]bool{
		ec2.NatGatewayStateDeleted:  true,
		ec2.NatGatewayStateDeleting: true,
		ec2.NatGatewayStateFailed:   true,
	}

	if _, ok := status[strings.ToLower(state)]; ngRaw == nil || ok {
		log.Printf("[INFO] Removing %s from Terraform state as it is not found or in the deleted state.", d.Id())
		d.SetId("")
		return nil
	}

	// Set NAT Gateway attributes
	ng := ngRaw.(*ec2.NatGateway)
	d.Set("connectivity_type", ng.ConnectivityType)
	d.Set("subnet_id", ng.SubnetId)

	// Address
	address := ng.NatGatewayAddresses[0]
	d.Set("allocation_id", address.AllocationId)
	d.Set("network_interface_id", address.NetworkInterfaceId)
	d.Set("private_ip", address.PrivateIp)
	d.Set("public_ip", address.PublicIp)

	tags := KeyValueTags(ng.Tags).IgnoreAWS().IgnoreConfig(ignoreTagsConfig)

	//lintignore:AWSR002
	if err := d.Set("tags", tags.RemoveDefaultConfig(defaultTagsConfig).Map()); err != nil {
		return fmt.Errorf("error setting tags: %w", err)
	}

	if err := d.Set("tags_all", tags.Map()); err != nil {
		return fmt.Errorf("error setting tags_all: %w", err)
	}

	return nil
}

func resourceNatGatewayUpdate(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).EC2Conn

	if d.HasChange("tags_all") {
		o, n := d.GetChange("tags_all")

		if err := UpdateTags(conn, d.Id(), o, n); err != nil {
			return fmt.Errorf("error updating EC2 NAT Gateway (%s) tags: %s", d.Id(), err)
		}
	}

	return resourceNatGatewayRead(d, meta)
}

func resourceNatGatewayDelete(d *schema.ResourceData, meta interface{}) error {
	conn := meta.(*conns.AWSClient).EC2Conn

	log.Printf("[INFO] Deleting EC2 NAT Gateway: %s", d.Id())
	_, err := conn.DeleteNatGateway(&ec2.DeleteNatGatewayInput{
		NatGatewayId: aws.String(d.Id()),
	})

	if tfawserr.ErrCodeEquals(err, ErrCodeNatGatewayNotFound) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("error deleting EC2 NAT Gateway (%s): %w", d.Id(), err)
	}

	if _, err := WaitNATGatewayDeleted(conn, d.Id()); err != nil {
		return fmt.Errorf("error waiting for EC2 NAT Gateway (%s) delete: %w", d.Id(), err)
	}

	return nil
}

// NGStateRefreshFunc returns a resource.StateRefreshFunc that is used to watch
// a NAT Gateway.
func NGStateRefreshFunc(conn *ec2.EC2, id string) resource.StateRefreshFunc {
	return func() (interface{}, string, error) {
		opts := &ec2.DescribeNatGatewaysInput{
			NatGatewayIds: []*string{aws.String(id)},
		}
		resp, err := conn.DescribeNatGateways(opts)
		if err != nil {
			if tfawserr.ErrMessageContains(err, "NatGatewayNotFound", "") {
				resp = nil
			} else {
				log.Printf("Error on NGStateRefresh: %s", err)
				return nil, "", err
			}
		}

		if resp == nil {
			// Sometimes AWS just has consistency issues and doesn't see
			// our instance yet. Return an empty state.
			return nil, "", nil
		}

		ng := resp.NatGateways[0]
		return ng, *ng.State, nil
	}
}
