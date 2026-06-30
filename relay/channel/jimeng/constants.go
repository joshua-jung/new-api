package jimeng

const (
	ChannelName = "jimeng"
)

var ModelList = []string{
	"jimeng_high_aes_general_v21_L", // 2.1 文生图（同步 CVProcess 接口）
	"jimeng_t2i_v30",                // 3.0 文生图（异步提交/查询接口）
	"jimeng_t2i_v31",                // 3.1 文生图
	"jimeng_i2i_v30",                // 3.0 图生图
	"jimeng_t2i_v40",                // 4.0 文生图/图像编辑 https://www.volcengine.com/docs/85621/1817045
	"jimeng_seedream46_cvtob",       // 4.6 文生图 https://www.volcengine.com/docs/85621/2275082（官方固定 req_key）
}
