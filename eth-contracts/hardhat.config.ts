import "@nomiclabs/hardhat-ethers";
import "hardhat-tracer";
import "@nomiclabs/hardhat-waffle";
import "solidity-coverage";
import "hardhat-gas-reporter";
import * as secrets from "./secrets.json";
import "./hardhat.tasks";

export default {
  solidity: {
    version: "0.8.3",
    settings: {
      optimizer: {
        enabled: false,
        runs: 200
      }
    }
  },
  networks: secrets.networks,
  gasReporter: {
    currency: 'USD',
    enabled: process.env.REPORT_GAS
  }
};