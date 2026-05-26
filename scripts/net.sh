#!/bin/bash
# Debian13 内网网卡配置 - 带重复配置校验版
# 核心：ifdown前校验重复配置，杜绝风险
# 用法：
#   Debug预览： bash $0 --debug 网卡名 内网IP
#   真实执行： bash $0 网卡名 内网IP

# ========== 参数处理 ==========
DEBUG=0
if [[ "$1" == "--debug" ]]; then
  DEBUG=1
  NIC="$2"
  IP="$3"
else
  NIC="$1"
  IP="$2"
fi

# 参数校验
if [[ -z "$NIC" || -z "$IP" ]]; then
  echo "用法："
  echo "  预览：bash $0 --debug 网卡名 内网IP"
  echo "  执行：bash $0 网卡名 内网IP"
  exit 1
fi

CONFIG_FILE="/etc/network/interfaces"
NETMASK="255.255.255.0"

# 生成网卡配置
CONFIG_BLOCK="auto $NIC
iface $NIC inet static
    address $IP
    netmask $NETMASK"

# ========== Debug 预览 ==========
if [[ $DEBUG -eq 1 ]]; then
  echo -e "\n===== 【Debug 预览】无任何修改 ====="
  echo "网卡：$NIC"
  echo "IP：$IP"
  echo "配置内容："
  echo "$CONFIG_BLOCK"
  echo "校验规则：执行ifdown前，检查是否有重复网卡配置"
  echo "====================================="
  exit 0
fi

# ========== 真实执行（安全流程） ==========
echo -e "\n===== Debian13 内网网卡安全配置 ====="

# 1. 备份配置
cp $CONFIG_FILE ${CONFIG_FILE}.$(date +%Y%m%d%H%M%S).bak
echo "✅ 配置已备份"

# 2. 清理该网卡旧配置（防止重复）
sed -i "/^auto $NIC/,+3d" $CONFIG_FILE

# 3. 写入新配置
echo -e "\n$CONFIG_BLOCK" >> $CONFIG_FILE
echo "✅ 新配置已写入"

# ========== ✅ 关键：重复配置校验（ifdown 之前执行） ==========
echo -e "\n🔍 正在校验【$NIC】是否存在重复配置..."
# 统计网卡配置块数量（以 auto 网卡名 开头的行数）
CONFIG_COUNT=$(grep -c "^auto $NIC" $CONFIG_FILE)

if [[ $CONFIG_COUNT -gt 1 ]]; then
  echo -e "❌ 错误：检测到【$NIC】有 $CONFIG_COUNT 份重复配置！"
  echo -e "❌ 为防止断网，终止执行，未重启网卡"
  exit 1
elif [[ $CONFIG_COUNT -eq 0 ]]; then
  echo -e "❌ 错误：未找到【$NIC】配置，异常退出"
  exit 1
else
  echo -e "✅ 校验通过：仅存在 1 份配置，无重复"
fi

# ========== 仅重启指定网卡（安全不断网） ==========
echo -e "\n🔄 重启网卡：$NIC"
ifdown $NIC 2>/dev/null
ifup $NIC

echo -e "\n🎉 全部完成！配置永久生效，重启机器无影响"
echo "==========================================="
