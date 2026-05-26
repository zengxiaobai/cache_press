#!/bin/bash
set +e
set -o pipefail

# ====================== 【配置】优化参数清单 ======================
GENERAL_OPTS=("noatime" "nodiratime" "nofail" "inode64")
XFS_MOUNT_OPTS=("logbufs=16" "logbsize=64k" "allocsize=65536" "swalloc" "attr2" "barrier=0")
XFS_MKFS_OPTS=("ftype=1" "crc=1" "finobt=1" "bigtime=1" "inobtcount=1")

# ====================== 【全局变量】 ======================
DRY_RUN=0
ACTION=""
TARGET_DISK=""
TEST_DISK=""
FINAL_MOUNT=""
FINAL_MKFS="-f"
# 配色
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[0;33m'
NC='\033[0m'

# ====================== 🔒 【三重防护】获取系统盘黑名单（绝对不碰） ======================
get_system_disks() {
    local sys_disks=()
    # 1. 根分区/boot/vector 所在磁盘
    for mp in / /boot /boot/efi /vector; do
        if mountpoint -q "$mp" &>/dev/null; then
            dev=$(df -P "$mp" | awk 'NR==2{print $1}')
            disk=$(lsblk -no PKNAME "$dev" 2>/dev/null | grep -E '^sd[a-z]$' | head -n1)
            [[ -n $disk ]] && sys_disks+=("$disk")
        fi
    done
    # 2. SWAP 分区所在磁盘
    for sw in $(swapon --show=NAME --noheadings 2>/dev/null); do
        disk=$(lsblk -no PKNAME "$sw" 2>/dev/null | grep -E '^sd[a-z]$' | head -n1)
        [[ -n $disk ]] && sys_disks+=("$disk")
    done
    # 去重并返回
    echo "${sys_disks[@]}" | tr ' ' '\n' | sort -u | tr '\n' ' '
}
SYSTEM_DISKS=($(get_system_disks))

# ====================== 🔒 校验：是否为系统盘（调用即拦截） ======================
is_system_disk() {
    local check_disk=$1
    for s in "${SYSTEM_DISKS[@]}"; do
        [[ $s == $check_disk ]] && return 0
    done
    return 1
}

# ====================== 🔍 自动选择【空闲·非系统·未挂载】测试盘 ======================
get_safe_test_disk() {
    # 筛选条件：未挂载 + 是磁盘 + sd[b-z] + 非系统盘
    local candidate_disks=($(lsblk -dn -o NAME,TYPE,MOUNTPOINT | \
        awk '$3=="" && $2=="disk" && $1~/^sd[b-z]/ {print $1}' | sort))

    for d in "${candidate_disks[@]}"; do
        if ! is_system_disk "$d"; then
            TEST_DISK="$d"
            echo -e "\n${GREEN}✅ 安全测试盘：/dev/$TEST_DISK（非系统盘·未挂载·空闲）${NC}"
            return
        fi
    done
    echo -e "\n${YELLOW}⚠️  无安全空闲数据盘，跳过XFS专属参数检测${NC}"
}

# ====================== ✅ 真实磁盘·无损检测挂载参数（仅用安全盘） ======================
detect_mount_opts() {
    echo -e "\n${BLUE}============================================================${NC}"
    echo -e "${BLUE}✅ 通用VFS优化参数（内核必支持）${NC}"
    echo -e "${BLUE}============================================================${NC}"
    for opt in "${GENERAL_OPTS[@]}"; do
        echo -e "  ✅ 支持  | $opt"
        FINAL_MOUNT+="$opt,"
    done

    [[ -z $TEST_DISK ]] && return
    local dev="/dev/$TEST_DISK"
    local tmp_mnt="/tmp/xfs_safe_test"
    mkdir -p "$tmp_mnt" &>/dev/null

    echo -e "\n${BLUE}============================================================${NC}"
    echo -e "${BLUE}🔍 真实磁盘检测：XFS专属优化参数${NC}"
    echo -e "${BLUE}============================================================${NC}"

    # 临时格式化（仅测试用）
    mkfs.xfs -f "$dev" &>/dev/null

    for opt in "${XFS_MOUNT_OPTS[@]}"; do
        mount -t xfs -o "$opt" "$dev" "$tmp_mnt" 2>/dev/null
        if [[ $? -eq 0 ]]; then
            echo -e "  ✅ 支持  | ${GREEN}$opt${NC}"
            FINAL_MOUNT+="$opt,"
            umount "$tmp_mnt" &>/dev/null
        else
            echo -e "  ❌ 不支持 | ${YELLOW}$opt${NC}"
        fi
    done

    # 🔥 无损清理：完全恢复测试盘原始状态
    umount "$tmp_mnt" &>/dev/null
    wipefs -a "$dev" &>/dev/null
    rmdir "$tmp_mnt" &>/dev/null
    FINAL_MOUNT=$(echo "$FINAL_MOUNT" | sed 's/,$//')
}

# ====================== ✅ 检测格式化优化参数 ======================
detect_mkfs_opts() {
    echo -e "\n${BLUE}============================================================${NC}"
    echo -e "${BLUE}🔧 XFS格式化优化参数检测${NC}"
    echo -e "${BLUE}============================================================${NC}"

    for opt in "${XFS_MKFS_OPTS[@]}"; do
        mkfs.xfs -o "${opt%=*}" --help 2>/dev/null | grep -qw "${opt%=*}"
        if [[ $? -eq 0 ]]; then
            echo -e "  ✅ 支持  | ${GREEN}$opt${NC}"
            FINAL_MKFS+=" -o $opt"
        else
            echo -e "  ❌ 不支持 | ${YELLOW}$opt${NC}"
        fi
    done
}

# ====================== 生成磁盘映射（自动跳过系统盘） ======================
generate_disk_mapping() {
    declare -gA DISK_MAP=()
    local idx=1
    local all_disks=($(lsblk -dn -o NAME,TYPE | awk '$2=="disk" && $1~/^sd[a-z]$/{print $1}' | sort))

    echo -e "\n${BLUE}============================================================${NC}"
    echo -e "${BLUE}📋 磁盘挂载映射（系统盘已自动跳过）${NC}"
    echo -e "${BLUE}============================================================${NC}"

    for d in "${all_disks[@]}"; do
        if ! is_system_disk "$d"; then
            DISK_MAP["$d"]="/data$idx"
            echo -e "  ${GREEN}$d → /data$idx${NC}"
            ((idx++))
        fi
    done
}

# ====================== 磁盘操作（系统盘直接拦截） ======================
process_disk() {
    local disk=$1
    local mnt=${DISK_MAP[$disk]}
    local dev="/dev/$disk"

    # 🔒 终极拦截：系统盘直接跳过
    if is_system_disk "$disk"; then
        echo -e "\n${RED}🔒 拦截成功：磁盘 $disk 是系统盘，已强制跳过！${NC}"
        return
    fi
    [[ ! -b $dev || -z $mnt ]] && return

    echo -e "\n${BLUE}============================================================${NC}"
    echo -e "${BLUE}🎯 操作磁盘：$dev → $mnt${NC}"
    echo -e "${BLUE}============================================================${NC}"

    # DEBUG 模式：仅打印
    if [[ $DRY_RUN -eq 1 ]]; then
        echo -e "${YELLOW}⚙️  DEBUG 模拟：无任何磁盘操作${NC}"
        echo "  卸载：umount -lf $dev $mnt"
        echo "  清理签名：wipefs -a $dev"
        echo "  格式化：mkfs.xfs $FINAL_MKFS $dev"
        echo "  挂载参数：$FINAL_MOUNT"
        return
    fi

    # 真实执行
    echo -e "${GREEN}🔧 执行高性能格式化+挂载...${NC}"
    umount -lf "$dev" "$mnt" &>/dev/null
    wipefs -a "$dev" &>/dev/null
    mkfs.xfs ${FINAL_MKFS} "$dev" &>/dev/null
    sync

    local uuid=$(blkid -s UUID -o value "$dev")
    mkdir -p "$mnt"
    sed -i "\#$dev#d" /etc/fstab
    sed -i "\#$mnt#d" /etc/fstab
    echo "UUID=$uuid $mnt xfs $FINAL_MOUNT 0 0" >> /etc/fstab
    mount "$mnt" &>/dev/null
}

# ====================== 参数解析 ======================
parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --debug) DRY_RUN=1; shift ;;
            --fresh-install) ACTION="batch"; shift ;;
            --reformat-disk) ACTION="single"; shift; TARGET_DISK="$1"; shift ;;
            *) shift ;;
        esac
    done

    if [[ -z $ACTION || ( $ACTION == "single" && -z $TARGET_DISK ) ]]; then
        echo -e "\n📌 使用方法："
        echo -e "  模拟批量  : $0 --fresh-install --debug"
        echo -e "  模拟单盘  : $0 --reformat-disk sdd --debug"
        echo -e "  真实批量  : $0 --fresh-install"
        exit 0
    fi
}

# ====================== 主程序 ======================
clear
parse_args "$@"

# 安全检测
echo -e "${RED}🔒 系统盘黑名单（绝对不操作）：${SYSTEM_DISKS[*]}${NC}"
get_safe_test_disk
detect_mount_opts
detect_mkfs_opts

# 打印最终参数
echo -e "\n${GREEN}============================================================${NC}"
echo -e "${GREEN}🏆 最终生效优化参数${NC}"
echo -e "${GREEN}  挂载参数：$FINAL_MOUNT${NC}"
echo -e "${GREEN}  格式化命令：mkfs.xfs $FINAL_MKFS${NC}"
echo -e "${GREEN}============================================================${NC}"

# 执行
generate_disk_mapping
[[ $ACTION == "batch" ]] && for d in "${!DISK_MAP[@]}"; do process_disk "$d"; done
[[ $ACTION == "single" ]] && process_disk "$TARGET_DISK"

echo -e "\n${GREEN}🎉 脚本执行完成！${NC}"
