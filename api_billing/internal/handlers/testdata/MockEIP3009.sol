// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

contract MockEIP3009 {
    event Transfer(address indexed from, address indexed to, uint256 value);

    function transferWithAuthorization(
        address from,
        address to,
        uint256 value,
        uint256,
        uint256,
        bytes32,
        uint8,
        bytes32,
        bytes32
    ) external {
        emit Transfer(from, to, value);
    }
}
